package runner

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/storage"
)

// SweepForeignContainers removes containers in the opendeploy containerd
// namespace that belong to no scheduled instance this node knows about: ids
// that do not follow the opendeploy-<dep>-<version>-<si>-<run> form, and ids
// whose deployment and scheduled instance are not in the store. Placement is
// the key rather than the full family so a rollover's previous spec version,
// which is live and owned by a known placement, is never touched. A foreign
// container whose task is still running is reported and left alone; only
// exited leftovers are deleted.
//
// Call it once the node's placements are known and before the deployment
// operator starts creating runners.
func SweepForeignContainers(ctx context.Context, store storage.OperatorStore, predicate storage.ScheduledInstancePredicate) {
	ctx = logu.AddTag(ctx, "ContainerSweep")
	states, _, unsubscribe := store.MustFetchScheduledSnapshotAndSubscribe(predicate)
	unsubscribe()
	known := make(map[placement]struct{}, len(states))
	for _, s := range states {
		known[placement{s.Instance.DeploymentID, s.Instance.ID}] = struct{}{}
	}
	containers, err := ctrd.Default.ListContainerStates(ctx)
	if err != nil {
		slog.WarnContext(ctx, "listing containers for the foreign container sweep failed", "err", err)
		return
	}
	removed, running := 0, 0
	for _, c := range containers {
		if !foreignContainer(c.ID, known) {
			continue
		}
		if c.TaskRunning {
			slog.WarnContext(ctx, fmt.Sprintf("foreign container %s has a running task; left in place", c.ID))
			running++
			continue
		}
		if err := ctrd.Default.Remove(ctx, c.ID); err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("removing foreign container %s failed", c.ID), "err", err)
			continue
		}
		slog.InfoContext(ctx, fmt.Sprintf("removed foreign container %s", c.ID))
		removed++
	}
	slog.InfoContext(ctx, fmt.Sprintf("foreign container sweep finished containers=%d known_placements=%d removed=%d running_foreign=%d", len(containers), len(known), removed, running))
}

type placement struct {
	deploymentID int32
	instanceID   int32
}

// foreignContainer reports whether a container id belongs to no known
// placement on this node.
func foreignContainer(id string, known map[placement]struct{}) bool {
	dep, _, instance, _, ok := parseContainerID(id)
	if !ok {
		return true
	}
	_, found := known[placement{dep, instance}]
	return !found
}

// parseContainerID splits opendeploy-<dep>-<version>-<si>-<run> into its
// parts. Every component must be a positive integer.
func parseContainerID(id string) (deploymentID, version, instanceID, run int32, ok bool) {
	parts := strings.Split(id, "-")
	if len(parts) != 5 || parts[0] != "opendeploy" {
		return 0, 0, 0, 0, false
	}
	var nums [4]int32
	for i, p := range parts[1:] {
		n, err := strconv.ParseInt(p, 10, 32)
		if err != nil || n <= 0 {
			return 0, 0, 0, 0, false
		}
		nums[i] = int32(n)
	}
	return nums[0], nums[1], nums[2], nums[3], true
}
