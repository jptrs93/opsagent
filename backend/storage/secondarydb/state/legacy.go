package state

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
)

// restoreCachedIdentity fills space/name on a config that carries them in
// neither flat field. Three generations of assignment data need this:
//
//  1. Blobs written by pre-v0.0.444 binaries, and live assignments sent by a
//     pre-v0.0.444 primary, carry the identity only in the legacy nested
//     layout — since v0.0.448 that layout decodes into Config.LegacyIdentity
//     and is folded into the flat fields here.
//  2. Blobs a pre-v0.0.444 worker re-encoded while connected to a v0.0.444–447
//     primary carry no identity in either format (the old decoder dropped the
//     flat fields as unknown, and those primaries did not dual-write the
//     legacy layout), so the internal deployments are recognized by their
//     specs instead. The worker must identify its netproxy deployment from
//     the local cache before it can reach the primary for a re-encoded
//     snapshot.
//
// User workloads in case 2 stay identityless until the first snapshot after
// reconnect; the boot sync gate keeps the operator from acting on them before
// that snapshot lands whenever the primary is reachable.
func restoreCachedIdentity(state *apigen.ScheduledInstanceState) {
	if state.Config.ID == 0 || state.Config.Name != "" {
		return
	}
	if legacy := state.Config.LegacyIdentity; legacy.Name != "" {
		state.Config.SpaceID = legacy.SpaceID
		state.Config.Name = legacy.Name
		return
	}
	if internaldeploy.IsSelfSpec(&state.Config.Spec) {
		state.Config.SpaceID = internaldeploy.SpaceID
		state.Config.Name = internaldeploy.SelfName
	} else if internaldeploy.IsNetproxySpec(&state.Config.Spec) {
		state.Config.SpaceID = internaldeploy.SpaceID
		state.Config.Name = internaldeploy.NetproxyName
	}
}
