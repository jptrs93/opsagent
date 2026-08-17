package state

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
)

// restoreCachedIdentity fills space/name on a cached config decoded without
// them. Two generations of blob need this: blobs written by pre-v0.0.444
// binaries carry the identity in a since-removed sub-message, and blobs a
// pre-v0.0.444 worker re-encoded while connected to a newer primary carry no
// identity at all (the old decoder dropped the flat fields as unknown), so the
// internal deployments are recognized by their specs instead. The worker must
// identify its netproxy deployment from this cache before it can reach the
// primary for a re-encoded snapshot.
func restoreCachedIdentity(state *apigen.ScheduledInstanceState, blob []byte) {
	if state.Config.ID == 0 || state.Config.Name != "" {
		return
	}
	if spaceID, name, ok := legacyDeploymentIdentity(blob); ok && name != "" {
		state.Config.SpaceID = spaceID
		state.Config.Name = name
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

// legacyDeploymentIdentity extracts (space_id, name) from the pre-v0.0.444
// DeploymentConfig.identity sub-message (field 3, since removed from the wire)
// inside a persisted ScheduledInstanceState blob.
func legacyDeploymentIdentity(stateBlob []byte) (spaceID int32, name string, ok bool) {
	cfgBytes, found := firstMessageField(stateBlob, 2)
	if !found {
		return 0, "", false
	}
	idBytes, found := firstMessageField(cfgBytes, 3)
	if !found {
		return 0, "", false
	}
	b := idBytes
	var num apigen.Number
	var typ apigen.Type
	var err error
	for len(b) > 0 {
		b, num, typ, err = apigen.ConsumeTag(b)
		if err != nil {
			return 0, "", false
		}
		switch num {
		case 1:
			b, spaceID, err = apigen.ConsumeVarInt32(b, typ)
		case 3:
			b, name, err = apigen.ConsumeString(b, typ)
		default:
			b, err = apigen.SkipFieldValue(b, num, typ)
		}
		if err != nil {
			return 0, "", false
		}
	}
	return spaceID, name, true
}

// firstMessageField returns the raw bytes of the first occurrence of a
// length-delimited field in an encoded message.
func firstMessageField(b []byte, field apigen.Number) ([]byte, bool) {
	var num apigen.Number
	var typ apigen.Type
	var err error
	for len(b) > 0 {
		b, num, typ, err = apigen.ConsumeTag(b)
		if err != nil {
			return nil, false
		}
		if num == field {
			_, msg, err := apigen.ConsumeMessage(b, typ)
			if err != nil {
				return nil, false
			}
			return msg, true
		}
		b, err = apigen.SkipFieldValue(b, num, typ)
		if err != nil {
			return nil, false
		}
	}
	return nil, false
}
