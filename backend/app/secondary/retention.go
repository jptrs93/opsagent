package secondary

import (
	"context"
	"log/slog"
	"time"

	"github.com/jptrs93/goutil/contextu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb"
)

// retentionInterval paces the sweep. Nothing depends on it being prompt — it
// reclaims disk and narrows what a stolen worker disk holds, neither of which is
// urgent — so it is slow enough to be invisible. A var so tests can shrink it.
var retentionInterval = 5 * time.Minute

// runRuntimeInputRetention periodically drops locally held secrets, configs and
// cached asset files that no instance assigned to this node references any more.
//
// It is the other half of local persistence: keeping runtime inputs across
// restarts is only acceptable if a node also stops holding what it no longer
// needs, so that removing a deployment from a node eventually removes its
// credentials from that node's disk too.
func runRuntimeInputRetention(ctx context.Context, store *secondarydb.Storage, inputs *runtimeinputs.RuntimeInputs, predicate storage.ScheduledInstancePredicate) {
	for {
		contextu.Sleep(ctx, retentionInterval)
		if ctx.Err() != nil {
			return
		}
		sweepRuntimeInputs(ctx, store, inputs, predicate)
	}
}

func sweepRuntimeInputs(ctx context.Context, store *secondarydb.Storage, inputs *runtimeinputs.RuntimeInputs, predicate storage.ScheduledInstancePredicate) {
	states := store.FetchScheduledSnapshot(predicate)

	// A mid-rollout instance is still running the previous config version, whose
	// referenced ids cannot be recovered from here: the node holds only the
	// current assignment blob, and the status carries the running version number
	// but not the config it came from. Sweeping then could delete an input that
	// the still-current container needs to respawn, so the sweep waits instead.
	// It is deliberately all-or-nothing: ids are shared between deployments, so
	// there is no sound way to attribute one to the instance that is settled.
	for i := range states {
		if !instanceQuiescent(&states[i]) {
			slog.DebugContext(ctx, "retention: skipping sweep, instance is mid-transition",
				"scheduled_instance", states[i].Instance.ID, "configVersion", states[i].Config.Version)
			return
		}
	}

	secrets := map[int32]struct{}{}
	configs := map[int32]struct{}{}
	assets := map[int32]struct{}{}
	for i := range states {
		cfg := &states[i].Config
		for _, id := range runtimeinputs.SecretRefs(cfg) {
			secrets[id] = struct{}{}
		}
		for _, id := range runtimeinputs.ConfigRefs(cfg) {
			configs[id] = struct{}{}
		}
		for _, ref := range runtimeinputs.RequiredAssetRefs(cfg) {
			assets[ref.AssetVersionID] = struct{}{}
		}
	}

	if removed, err := inputs.Retain(secrets, configs); err != nil {
		slog.WarnContext(ctx, "retention: dropping unreferenced runtime inputs failed", "err", err)
	} else if removed > 0 {
		slog.InfoContext(ctx, "retention: dropped unreferenced runtime inputs", "count", removed)
	}

	if removed, err := runtimeinputs.RetainAssets(assets); err != nil {
		slog.WarnContext(ctx, "retention: dropping unreferenced cached assets failed", "err", err)
	} else if removed > 0 {
		slog.InfoContext(ctx, "retention: dropped unreferenced cached assets", "count", removed)
	}
}

// instanceQuiescent reports whether an instance has finished converging, so that
// nothing it previously referenced can still be needed.
//
// An instance that should not be running is always quiescent: with no container
// alive there is no older config version left to serve, and its own refs are
// retained regardless because it still contributes them.
func instanceQuiescent(state *apigen.ScheduledInstanceState) bool {
	if !state.Instance.State.WantsRunning() {
		return true
	}
	if state.Status.Preparer.DeploymentConfigVersion != state.Config.Version ||
		state.Status.Preparer.Rollup() != apigen.PreparationStatus_READY {
		return false
	}
	return state.Status.Runner.DeploymentConfigVersion == state.Config.Version
}
