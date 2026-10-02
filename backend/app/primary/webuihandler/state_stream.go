package webuihandler

import (
	"iter"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

type opening struct {
	seq      int64
	entities []*apigen.MaterialisedEntity
}

// openSnapshotLocked reads the state a subscriber starts from: the
// materialised snapshot at the current seq. The caller holds h.Store.Mu so
// the read and the live subscription see the same seq.
func (h *Handler) openSnapshotLocked(ctx apigen.Context) (opening, error) {
	q := h.Store.Queries()
	seq, err := q.GetGlobalSeq(ctx)
	if err != nil {
		return opening{}, err
	}
	entities, err := q.Snapshot(ctx)
	return opening{seq: seq, entities: entities}, err
}

func (h *Handler) openSnapshot(ctx apigen.Context) (opening, error) {
	h.Store.Mu.Lock()
	defer h.Store.Mu.Unlock()
	return h.openSnapshotLocked(ctx)
}

func openingMsg(visibility *streamVisibility, op opening) *apigen.EventStreamMsg {
	visibility.reset()
	snapshot := &apigen.CoreSnapshot{Seq: op.seq, Entities: visibility.visibleSnapshot(op.entities)}
	return &apigen.EventStreamMsg{Seq: op.seq, Synced: true, Snapshot: snapshot}
}

func (h *Handler) sidecarMsg(ctx apigen.Context, secrets apigen.SecretsStatusResponse, backup apigen.BackupStatus, diagnostics *apigen.IngressDiagnosticList) *apigen.EventStreamMsg {
	if !h.canAccess(ctx, vView, eCluster, 0, 0) {
		backup = apigen.BackupStatus{}
	}
	return &apigen.EventStreamMsg{SecretsStatus: &secrets, BackupStatus: &backup, IngressDiagnostics: h.filterIngressDiagnostics(ctx, diagnostics)}
}

func (h *Handler) PostV1GlobalEvents(ctx apigen.Context, req *apigen.EventStreamRequest) (*apigen.EventStreamMsg, error) {
	op, err := h.openSnapshot(ctx)
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

// PostV1GlobalEventStream sends the sidecar statuses, the opening snapshot,
// then every later commit as it happens. A commit that can change what the
// viewer may see (a grant, a space move) is not forwarded; after a short
// debounce the stream sends a fresh snapshot instead, which the browser
// applies as its whole state. Every message carries the seq the browser is
// now at.
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
			out, err := h.openSnapshotLocked(ctx)
			openErr = err
			return out
		}, func(u state.WriteUpdate) (state.WriteUpdate, bool) { return u, true })
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
		send := func(update state.WriteUpdate) bool {
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
				current, err := h.openSnapshot(ctx)
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
