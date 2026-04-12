package handler

import (
	"iter"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// PostV1StateStream delivers the current deployment snapshot + user config
// to the UI, then forwards per-deployment updates as they happen. Passing
// "" for the machine asks the store for all deployments across the cluster.
//
// TODO: user-config change notification. The store currently only exposes
// a one-shot fetch, so a yaml save won't auto-refresh connected UIs until
// they reconnect. Add a subscribe method in a later pass.
func (h *Handler) PostV1StateStream(ctx apigen.Context) iter.Seq2[*apigen.State, error] {
	return func(yield func(*apigen.State, error) bool) {
		snapshot, updatesCh := h.Store.MustFetchSnapshotAndSubscribe(ctx, "")
		userSub, userUnsub := h.Store.SubscribeUserUpdates()
		defer userUnsub()

		items := make([]*apigen.DeploymentWithStatus, 0, len(snapshot))
		for i := range snapshot {
			items = append(items, &snapshot[i])
		}
		initial := &apigen.State{
			DeploymentsSnapshot: &apigen.DeploymentWithStatusSnapshot{Items: items},
			UserConfigSnapshot:  h.Store.MustFetchUserConfigVersion(),
			UsersSnapshot:       h.Store.ListUsersPublic(),
		}
		if !yield(initial, nil) {
			return
		}

		heartbeatTicker := time.NewTicker(5 * time.Second)
		defer heartbeatTicker.Stop()

		for {
			select {
			case <-ctx.Done():
				return
			case dws, ok := <-updatesCh:
				if !ok {
					return
				}
				update := dws
				if !yield(&apigen.State{DeploymentUpdate: &update}, nil) {
					return
				}
			case u, ok := <-userSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{UserUpdate: &u}, nil) {
					return
				}
			case <-heartbeatTicker.C:
				if !yield(&apigen.State{Heartbeat: true}, nil) {
					return
				}
			}
		}
	}
}
