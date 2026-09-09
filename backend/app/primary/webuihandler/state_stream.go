package webuihandler

import (
	"iter"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// One connection multiplexes independently owned state contexts. Closing any
// subscription reconnects the browser with a fresh snapshot of every context.
func (h *Handler) PostV1GlobalStateStream(ctx apigen.Context) iter.Seq2[*apigen.StateStreamMsg, error] {
	return func(yield func(*apigen.StateStreamMsg, error) bool) {
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
		userID := int32(0)
		if ctx.User != nil {
			userID = ctx.User.ID
		}
		agentSessions, agentSub, err := h.agentSessions().SnapshotAndSubscribe(userID)
		if err != nil {
			yield(nil, err)
			return
		}
		defer agentSub.Unsubscribe()
		diagnostics := &apigen.IngressDiagnosticList{}
		var diagnosticsCh <-chan *apigen.IngressDiagnosticList
		if h.IngressDiagnostics != nil {
			var unsubscribe func()
			diagnostics, diagnosticsCh, unsubscribe = h.IngressDiagnostics.DiagnosticsSnapshotAndSubscribe()
			defer unsubscribe()
		}
		visibility := newStreamVisibility(h, ctx)
		decorate := func(out *apigen.Snapshot) *apigen.Snapshot {
			secretCopy, backupCopy := secretsStatus, backupStatus
			out.SecretsStatus, out.BackupStatus = &secretCopy, &backupCopy
			out.AgentSessions, out.IngressDiagnostics = agentSessions, diagnostics
			return visibility.visibleSnapshot(out)
		}
		raw, updates, unsubscribe := state.Subscribe(h.Store, func() *apigen.Snapshot { return state.BuildSnapshot(ctx, h.Store.Queries()) }, func(u state.Update) (state.Update, bool) { return u, true })
		defer unsubscribe()
		snapshot := func() *apigen.Snapshot { return decorate(h.Store.BuildSnapshot(ctx)) }
		initial := decorate(raw)
		if !yield(&apigen.StateStreamMsg{Snapshot: initial}, nil) {
			return
		}
		seq := initial.Seq
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
			if visibility.needsReset(update) {
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
			if visible := visibility.visibleUpdate(&update); visible != nil && !yield(&apigen.StateStreamMsg{Core: visible}, nil) {
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
				if h.canAccess(ctx, vView, eCluster, 0, 0) && !yield(&apigen.StateStreamMsg{BackupStatus: &status}, nil) {
					return
				}
			case status, ok := <-secretSub.Ch:
				if !ok {
					return
				}
				secretsStatus = status
				if !yield(&apigen.StateStreamMsg{SecretsStatus: &status}, nil) {
					return
				}
			case update, ok := <-agentSub.Ch:
				if !ok {
					return
				}
				agentSessions = update.Sessions
				if !yield(&apigen.StateStreamMsg{AgentSessions: &apigen.AgentSessionList{Items: agentSessions}}, nil) {
					return
				}
			case next, ok := <-diagnosticsCh:
				if !ok {
					return
				}
				diagnostics = next
				if !yield(&apigen.StateStreamMsg{IngressDiagnostics: h.filterIngressDiagnostics(ctx, next)}, nil) {
					return
				}
			case <-resetC:
				current := snapshot()
				seq = current.Seq
				reset, resetC = nil, nil
				if !yield(&apigen.StateStreamMsg{Snapshot: current}, nil) {
					return
				}
			case <-heartbeat.C:
				if !yield(&apigen.StateStreamMsg{Heartbeat: true}, nil) {
					return
				}
			}
		}
	}
}
