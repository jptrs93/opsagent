// Package instancecache holds the in-memory scheduled-instance runtime view
// shared by the primary and secondary storage layers: the authoritative
// per-instance state cache, its update stream, and the status write path.
//
// It exists so the two role-specific storage packages (primarydb, secondarydb)
// can stay fully independent at the schema and query level without duplicating
// the subtle parts — the status HLC merge and the publish-before-remove
// ordering — whose divergence would be a real bug source.
//
// Locking discipline: fields are exported so the owning storage package can
// compose its own operations. Any access to Scheduled or call to a *Locked
// method must hold Mu.
package instancecache

import (
	"context"
	"fmt"
	"log/slog"
	"sync"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/goutil/pubsubu"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage"
)

type Cache struct {
	Mu sync.Mutex

	// Scheduled holds the authoritative runtime view per scheduled instance id:
	// assignment row, pinned spec version, and latest status. Live instances
	// only — a finalized instance is removed, and every consumer that reconciles
	// or routes depends on that.
	Scheduled map[int32]*apigen.ScheduledInstanceState

	Subs *pubsubu.PubSub[apigen.ScheduledInstanceState]

	// PersistStatus durably appends a status row. Each role wires its own
	// query layer here; failures are the hook's to handle (both roles panic,
	// per the storage error policy).
	PersistStatus func(ctx context.Context, st *apigen.ScheduledInstanceStatus)
}

func New(persistStatus func(ctx context.Context, st *apigen.ScheduledInstanceStatus)) *Cache {
	return &Cache{
		Scheduled:     make(map[int32]*apigen.ScheduledInstanceState),
		Subs:          &pubsubu.PubSub[apigen.ScheduledInstanceState]{},
		PersistStatus: persistStatus,
	}
}

func (c *Cache) FetchScheduledSnapshot(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	return c.SnapshotLocked(predicate)
}

func (c *Cache) MustFetchScheduledSnapshotAndSubscribe(predicate storage.ScheduledInstancePredicate) ([]apigen.ScheduledInstanceState, chan apigen.ScheduledInstanceState, func()) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	snapshot := c.SnapshotLocked(predicate)
	sub := c.Subs.Subscribe(InstanceFilter(predicate))
	return snapshot, sub.Ch, sub.Unsubscribe
}

func (c *Cache) SubscribeScheduledInstanceUpdates(predicate storage.ScheduledInstancePredicate) (chan apigen.ScheduledInstanceState, func()) {
	sub := c.Subs.Subscribe(InstanceFilter(predicate))
	return sub.Ch, sub.Unsubscribe
}

func (c *Cache) MustWriteScheduledInstanceStatus(instanceID int32, f func(*apigen.ScheduledInstanceStatus) bool) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	ctx := logu.AddTag(context.Background(), "Store")
	ctx = logu.AddKV(ctx, "scheduled_instance", instanceID)

	state := c.Scheduled[instanceID]
	if state == nil {
		slog.WarnContext(ctx, "status write for unknown scheduled instance")
		return
	}

	current := state.Status
	if current.ScheduledInstanceID == 0 {
		current.ScheduledInstanceID = instanceID
		current.DeploymentID = state.Instance.DeploymentID
	}

	if !f(&current) {
		return
	}
	current.ScheduledInstanceID = instanceID
	current.DeploymentID = state.Instance.DeploymentID

	c.PersistStatus(ctx, &current)

	state.Status = current
	slog.InfoContext(ctx, fmt.Sprintf("scheduled instance status published updatedAt=%v preparerStatus=%v runnerStatus=%v",
		current.UpdatedAt, current.Preparer.Rollup(), current.Runner.Status))
	c.NotifyInstanceLocked(instanceID)
}

// FetchScheduledInstance returns the assignment alone. Callers reconciling a
// decision made earlier use it to confirm the placement still exists and is
// still in the state they left it in.
func (c *Cache) FetchScheduledInstance(instanceID int32) *apigen.ScheduledInstance {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	state := c.Scheduled[instanceID]
	if state == nil {
		return nil
	}
	cp := state.Instance
	return &cp
}

func (c *Cache) FetchScheduledInstanceStatus(instanceID int32) *apigen.ScheduledInstanceStatus {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	state := c.Scheduled[instanceID]
	if state == nil || state.Status.IsZero() {
		return nil
	}
	cp := state.Status
	return &cp
}

func (c *Cache) SnapshotLocked(predicate storage.ScheduledInstancePredicate) []apigen.ScheduledInstanceState {
	out := make([]apigen.ScheduledInstanceState, 0, len(c.Scheduled))
	for id, state := range c.Scheduled {
		if state.Instance.State == apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED {
			continue
		}
		item := c.StateLocked(id)
		if predicate != nil && !predicate(item) {
			continue
		}
		out = append(out, item)
	}
	return out
}

func (c *Cache) StateLocked(id int32) apigen.ScheduledInstanceState {
	state := c.Scheduled[id]
	if state == nil {
		return apigen.ScheduledInstanceState{}
	}
	out := *state
	if out.Config.ID != 0 {
		out.Status = WithRunningVersion(&out.Config, out.Status)
	}
	return out
}

func (c *Cache) NotifyInstanceLocked(id int32) {
	state := c.StateLocked(id)
	if state.Instance.ID == 0 {
		return
	}
	name := ""
	if state.Config.Def.Name != "" {
		name = fmt.Sprintf("%d:%d:%s", state.Config.Def.SpaceID, state.Config.Def.NodeID, state.Config.Def.Name)
	}
	ctx := logu.AddTag(context.Background(), "Store")
	slog.InfoContext(ctx, fmt.Sprintf("store: notify scheduled instance name=%s configVersion=%d targetState=%v hasPreparer=%t hasRunner=%t",
		name, state.Instance.DeploymentSpecVersion, state.Instance.State,
		!state.Status.Preparer.IsZero(), !state.Status.Runner.IsZero()),
		"scheduled_instance", id,
		"dep", state.Instance.DeploymentID,
	)
	c.Subs.Notify(state)
}

func InstanceFilter(predicate storage.ScheduledInstancePredicate) func(apigen.ScheduledInstanceState, apigen.ScheduledInstanceState) bool {
	if predicate == nil {
		return nil
	}
	return func(_, state apigen.ScheduledInstanceState) bool {
		return predicate(state)
	}
}

// WithRunningVersion decorates a status with the config's workload version when
// the runner is on the current config, so display layers never derive it.
func WithRunningVersion(cfg *apigen.Deployment, st apigen.ScheduledInstanceStatus) apigen.ScheduledInstanceStatus {
	if st.Runner.IsZero() || cfg == nil {
		return st
	}
	ver := st.Runner.DeploymentSpecVersion
	if ver == 0 {
		return st
	}
	if ver == cfg.SpecVersion {
		st.Runner.RunningVersion = cfg.WorkloadVersion()
	}
	return st
}
