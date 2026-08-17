package state

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

// legacyDeploymentIdentity extracts (space_id, name) from the pre-v0.0.444
// DeploymentConfig.identity sub-message (field 3, since removed from the wire)
// inside a persisted ScheduledInstanceState blob. Assignment blobs written by
// older binaries survive an upgrade, and the worker must identify its netproxy
// deployment from this cache before it can reach the primary for a re-encoded
// snapshot, so the removed field has to stay readable here.
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
