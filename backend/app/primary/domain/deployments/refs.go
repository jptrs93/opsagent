package deployments

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

var ReferenceInUseErr = apigen.NewApiErr("Referenced value is still in use", "reference_in_use", 400)

// ReferenceInUseDetailErr is ReferenceInUseErr with a display message naming
// what still pins the value, e.g. "Secret still in use: referenced by
// deployment global / dev-machine / api". ApiErr.Is matches on the internal
// code, so errors.Is against the bare sentinel still holds.
func ReferenceInUseDetailErr(subject string, details []string) error {
	if len(details) == 0 {
		return ReferenceInUseErr
	}
	const maxShown = 5
	if len(details) > maxShown {
		details = append(details[:maxShown:maxShown], fmt.Sprintf("%d more", len(details)-maxShown))
	}
	return apigen.NewApiErr(
		subject+" still in use: referenced by "+strings.Join(details, "; "),
		ReferenceInUseErr.InternalErr,
		ReferenceInUseErr.Code,
	)
}

// MoveReferencesOutsideSpaceErr refuses a cross-space move while the value is
// pinned from outside the destination space — by a deployment in another
// space, or by cluster settings (which pin the value to the global space).
var MoveReferencesOutsideSpaceErr = apigen.NewApiErr("Value is referenced from outside the destination space", "move_references_outside_space", 400)

// The deployment-side scans below extract pinned version ids with the same
// collectors the engine uses to fetch runtime inputs (plus AddressRefIDs,
// which has no engine collector because addresses are not fetched). Delete and
// move protection therefore cannot lag behind what a runner would actually
// resolve — the env-only scan this replaced missed ingress cert secrets.

func AssetRefIDs(cfg *apigen.DeploymentEvent) []int32 {
	refs := runtimeinputs.RequiredAssetRefs(cfg)
	ids := make([]int32, 0, len(refs))
	for _, ref := range refs {
		ids = append(ids, ref.AssetVersionID)
	}
	return ids
}

func AddressRefIDs(cfg *apigen.DeploymentEvent) []int32 {
	container := cfg.Value.Spec.Container()
	if container == nil {
		return nil
	}
	var ids []int32
	for _, value := range container.Runtime.EnvVars {
		if value != nil && value.AddressDeploymentID != nil {
			ids = append(ids, *value.AddressDeploymentID)
		}
	}
	return ids
}

func CrossDeploymentMountSourceIDs(cfg *apigen.DeploymentEvent) []int32 {
	container := cfg.Value.Spec.Container()
	if container == nil {
		return nil
	}
	var ids []int32
	for _, mount := range container.Runtime.CrossDeploymentMounts {
		if mount != nil {
			ids = append(ids, mount.DeploymentID)
		}
	}
	return ids
}

// Referencing returns the non-deleted deployments pinning any of
// ids, with refs extracting one kind's version ids from a config.
func Referencing(live nodes.LiveState, ids map[int32]struct{}, refs func(*apigen.DeploymentEvent) []int32) []*apigen.DeploymentEvent {
	var out []*apigen.DeploymentEvent
	for _, cfg := range live.Deployments {
		for _, id := range refs(cfg) {
			if _, ok := ids[id]; ok {
				out = append(out, cfg)
				break
			}
		}
	}
	return out
}

// ReferencesOutsideSpace reports whether any non-deleted deployment outside
// spaceID pins one of ids — the veto for cross-space moves.
func ReferencesOutsideSpace(live nodes.LiveState, ids map[int32]struct{}, refs func(*apigen.DeploymentEvent) []int32, spaceID int32) bool {
	for _, cfg := range Referencing(live, ids, refs) {
		if cfg.Value.SpaceID != spaceID {
			return true
		}
	}
	return false
}

func UsesAddressID(live nodes.LiveState, ids map[int32]struct{}) bool {
	return len(Referencing(live, ids, AddressRefIDs)) > 0
}

// RefDetails renders "deployment <space> / <node> / <name>" lines
// for every deployment pinning one of ids — the human-readable half of the
// reference_in_use refusal.
func RefDetails(ctx context.Context, q *pq.Queries, live nodes.LiveState, ids map[int32]struct{}, refs func(*apigen.DeploymentEvent) []int32) []string {
	cfgs := Referencing(live, ids, refs)
	if len(cfgs) == 0 {
		return nil
	}
	spaces := map[int32]string{}
	rows, err := q.ListSpaces(ctx)
	if err != nil {
		panic(err)
	}
	for _, space := range rows {
		spaces[int32(space.ID)] = space.Name
	}
	nodes := map[int32]string{}
	for _, node := range live.Nodes {
		nodes[node.ID] = node.Name
	}
	details := make([]string, 0, len(cfgs))
	for _, cfg := range cfgs {
		space := spaces[cfg.Value.SpaceID]
		if space == "" {
			space = fmt.Sprintf("space %d", cfg.Value.SpaceID)
		}
		node := nodes[cfg.Value.NodeID]
		if node == "" {
			node = fmt.Sprintf("node %d", cfg.Value.NodeID)
		}
		details = append(details, "deployment "+space+" / "+node+" / "+cfg.Value.Name)
	}
	sort.Strings(details)
	return details
}

func Int32Set(ids []int32) map[int32]struct{} {
	out := make(map[int32]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out
}
