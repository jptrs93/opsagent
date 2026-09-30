package webuihandler

import (
	"iter"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// maxReplaySeqs bounds a reconnect replay; a browser further behind gets the
// compacted history instead.
const maxReplaySeqs = 10_000

type opening struct {
	seq    int64
	reset  bool
	events []*apigen.CoreWriteUpdate
}

// openEventsLocked reads the events a subscriber starts from: a replay of
// everything after afterSeq when that window is small and nothing in it can
// have changed what the viewer may see, otherwise the compacted history with
// reset set. The caller holds h.Store.Mu so the read and the live
// subscription see the same seq.
func (h *Handler) openEventsLocked(ctx apigen.Context, afterSeq int64) (opening, error) {
	q := h.Store.Queries()
	seq, err := q.GetGlobalSeq(ctx)
	if err != nil {
		return opening{}, err
	}
	bootstrap := afterSeq <= 0 || afterSeq > seq || seq-afterSeq > maxReplaySeqs
	if !bootstrap {
		changed, err := q.VisibilityChangesSince(ctx, afterSeq, userIDOf(ctx))
		if err != nil {
			return opening{}, err
		}
		bootstrap = changed
	}
	if bootstrap {
		ms, err := q.BootstrapMutations(ctx)
		return opening{seq: seq, reset: true, events: pq.Events(ms)}, err
	}
	ms, err := q.MutationsInRange(ctx, afterSeq, seq)
	return opening{seq: seq, events: pq.Events(ms)}, err
}

func (h *Handler) openEvents(ctx apigen.Context, afterSeq int64) (opening, error) {
	h.Store.Mu.Lock()
	defer h.Store.Mu.Unlock()
	return h.openEventsLocked(ctx, afterSeq)
}

func userIDOf(ctx apigen.Context) int64 {
	if ctx.User == nil {
		return 0
	}
	return int64(ctx.User.ID)
}

func openingMsg(visibility *streamVisibility, op opening) *apigen.EventStreamMsg {
	if op.reset {
		visibility.reset()
	}
	return &apigen.EventStreamMsg{Seq: op.seq, Reset: op.reset, Synced: true, Events: visibility.visibleEvents(op.events)}
}

func (h *Handler) sidecarMsg(ctx apigen.Context, secrets apigen.SecretsStatusResponse, backup apigen.BackupStatus, diagnostics *apigen.IngressDiagnosticList) *apigen.EventStreamMsg {
	if !h.canAccess(ctx, vView, eCluster, 0, 0) {
		backup = apigen.BackupStatus{}
	}
	return &apigen.EventStreamMsg{SecretsStatus: &secrets, BackupStatus: &backup, IngressDiagnostics: h.filterIngressDiagnostics(ctx, diagnostics)}
}

func (h *Handler) PostV1GlobalEvents(ctx apigen.Context, req *apigen.EventStreamRequest) (*apigen.EventStreamMsg, error) {
	op, err := h.openEvents(ctx, req.AfterSeq)
	if err != nil {
		return nil, err
	}
	msg := openingMsg(newStreamVisibility(h, ctx), op)
	status, ok := h.secretsUpdates.ValueOK()
	if !ok {
		status = h.secretsStatus()
	}
	backup := apigen.BackupStatus{}
	if h.BackupStatus != nil {
		backup = h.BackupStatus.Snapshot()
	}
	var diagnostics *apigen.IngressDiagnosticList
	if h.IngressDiagnostics != nil {
		initial, _, unsubscribe := h.IngressDiagnostics.DiagnosticsSnapshotAndSubscribe()
		unsubscribe()
		diagnostics = initial
	}
	side := h.sidecarMsg(ctx, status, backup, diagnostics)
	msg.SecretsStatus, msg.BackupStatus, msg.IngressDiagnostics = side.SecretsStatus, side.BackupStatus, side.IngressDiagnostics
	return msg, nil
}

// PostV1GlobalEventStream sends the opening events for after_seq, the
// sidecar statuses, then every later commit as it happens. A commit that can
// change what the viewer may see (a grant, a space move) is not forwarded;
// after a short debounce the stream sends the compacted history again with
// reset set. Every message carries the seq the browser is now at.
func (h *Handler) PostV1GlobalEventStream(ctx apigen.Context, req *apigen.EventStreamRequest) iter.Seq2[*apigen.EventStreamMsg, error] {
	return func(yield func(*apigen.EventStreamMsg, error) bool) {
		secretSub := h.secretsUpdates.Subscribe(nil)
		defer secretSub.Unsubscribe()
		secretsStatus := secretSub.InitialValue
		if !secretSub.InitialValueValid {
			secretsStatus = h.secretsStatus()
		}
		backupStatus := apigen.BackupStatus{}
		var backupCh <-chan apigen.BackupStatus
		if h.BackupStatus != nil {
			sub := h.BackupStatus.SnapshotAndSubscribe()
			defer sub.Unsubscribe()
			backupStatus, backupCh = sub.InitialValue, sub.Ch
		}
		diagnostics := &apigen.IngressDiagnosticList{}
		var diagnosticsCh <-chan *apigen.IngressDiagnosticList
		if h.IngressDiagnostics != nil {
			var unsubscribe func()
			diagnostics, diagnosticsCh, unsubscribe = h.IngressDiagnostics.DiagnosticsSnapshotAndSubscribe()
			defer unsubscribe()
		}
		visibility := newStreamVisibility(h, ctx)
		var openErr error
		op, updates, unsubscribe := state.Subscribe(h.Store, func() opening {
			out, err := h.openEventsLocked(ctx, req.AfterSeq)
			openErr = err
			return out
		}, func(u state.Update) (state.Update, bool) { return u, true })
		defer unsubscribe()
		if openErr != nil {
			yield(nil, openErr)
			return
		}
		if !yield(h.sidecarMsg(ctx, secretsStatus, backupStatus, diagnostics), nil) {
			return
		}
		if !yield(openingMsg(visibility, op), nil) {
			return
		}
		seq := op.seq
		heartbeat := time.NewTicker(5 * time.Second)
		defer heartbeat.Stop()
		var reset *time.Timer
		var resetC <-chan time.Time
		defer func() {
			if reset != nil {
				reset.Stop()
			}
		}()
		send := func(update state.Update) bool {
			if update.Seq <= seq {
				return true
			}
			if visibility.needsReset(&update) {
				if reset == nil {
					reset = time.NewTimer(200 * time.Millisecond)
					resetC = reset.C
				}
				return true
			}
			if resetC != nil {
				return true
			}
			seq = update.Seq
			if visible := visibility.visibleUpdate(&update); visible != nil && !yield(&apigen.EventStreamMsg{Seq: seq, Events: []*apigen.CoreWriteUpdate{visible}}, nil) {
				return false
			}
			return true
		}
		for {
			select {
			case <-ctx.Done():
				return
			case update, ok := <-updates:
				if !ok || !send(update) {
					return
				}
			case status, ok := <-backupCh:
				if !ok {
					return
				}
				backupStatus = status
				if h.canAccess(ctx, vView, eCluster, 0, 0) && !yield(&apigen.EventStreamMsg{BackupStatus: &status}, nil) {
					return
				}
			case status, ok := <-secretSub.Ch:
				if !ok {
					return
				}
				secretsStatus = status
				if !yield(&apigen.EventStreamMsg{SecretsStatus: &status}, nil) {
					return
				}
			case next, ok := <-diagnosticsCh:
				if !ok {
					return
				}
				diagnostics = next
				if !yield(&apigen.EventStreamMsg{IngressDiagnostics: h.filterIngressDiagnostics(ctx, next)}, nil) {
					return
				}
			case <-resetC:
				reset, resetC = nil, nil
				current, err := h.openEvents(ctx, 0)
				if err != nil {
					yield(nil, err)
					return
				}
				seq = current.seq
				if !yield(openingMsg(visibility, current), nil) {
					return
				}
			case <-heartbeat.C:
				if !yield(&apigen.EventStreamMsg{Seq: seq, Heartbeat: true}, nil) {
					return
				}
			}
		}
	}
}
