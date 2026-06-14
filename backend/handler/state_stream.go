package handler

import (
	"iter"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// PostV1StateStream delivers the current deployment snapshot to the UI,
// then forwards per-deployment updates as they happen, with periodic
// heartbeats to keep the HTTP connection alive.
func (h *Handler) PostV1StateStream(ctx apigen.Context) iter.Seq2[*apigen.State, error] {
	return func(yield func(*apigen.State, error) bool) {
		snapshot, updatesCh, updatesUnsub := h.Store.MustFetchSnapshotAndSubscribe("")
		defer updatesUnsub()
		userSub, userUnsub := h.Store.SubscribeUserUpdates()
		defer userUnsub()
		enrollments, enrollmentCh, enrollmentUnsub, err := h.Store.MustFetchEnrollmentSnapshotAndSubscribe()
		if err != nil {
			yield(nil, err)
			return
		}
		defer enrollmentUnsub()

		machines := []*apigen.ClusterMachine{{
			Name:      h.MachineName,
			IsPrimary: true,
			Connected: true,
		}}
		var machineCh chan apigen.ClusterMachine
		var machineUnsub func()
		if h.ClusterPrimary != nil {
			workerMachines, ch, unsub := h.ClusterPrimary.FetchMachinesSnapshotAndSubscribe()
			machines = append(machines, workerMachines...)
			machineCh = ch
			machineUnsub = unsub
		}
		if machineUnsub != nil {
			defer machineUnsub()
		}

		items := make([]*apigen.DeploymentWithStatus, 0, len(snapshot))
		for i := range snapshot {
			items = append(items, redactDeploymentWithStatus(&snapshot[i]))
		}
		initial := &apigen.State{
			DeploymentsSnapshot: &apigen.DeploymentWithStatusSnapshot{Items: items},
			UsersSnapshot:       h.Store.ListUsersPublic(),
			MachinesSnapshot:    &apigen.ClusterMachineList{Items: machines},
			EnrollmentsSnapshot: &apigen.EnrollmentRequestList{Items: enrollments},
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
				update := redactDeploymentWithStatus(&dws)
				if !yield(&apigen.State{DeploymentUpdate: update}, nil) {
					return
				}
			case u, ok := <-userSub.Ch:
				if !ok {
					return
				}
				if !yield(&apigen.State{UserUpdate: &u}, nil) {
					return
				}
			case machine, ok := <-machineCh:
				if !ok {
					return
				}
				if !yield(&apigen.State{MachineUpdate: &machine}, nil) {
					return
				}
			case enrollment, ok := <-enrollmentCh:
				if !ok {
					return
				}
				if !yield(&apigen.State{EnrollmentUpdate: &enrollment}, nil) {
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
