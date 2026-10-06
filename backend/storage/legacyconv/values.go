package legacyconv

import (
	"encoding/hex"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
)

func Secret(old *apigenold.Secret) (*apigen.Secret, error) {
	c := &conv{}
	if old == nil {
		c.refuse("Secret", "", "nil payload")
		return checked("Secret", (*apigen.Secret)(nil), c.err)
	}
	out := &apigen.Secret{
		ID:      c.id("Secret", "id", int64(old.ID)),
		SpaceID: c.id("Secret", "space_id", int64(old.SpaceID)),
	}
	if old.Fs == nil {
		c.refuse("Secret", "fs", "unset")
	} else {
		out.Fs = apigen.SecretFs{Key: old.Fs.Name, DirectoryID: c.optID("SecretFs", "directory_id", int64(old.Fs.DirectoryID))}
	}
	if len(old.Ciphertext) > 0 {
		out.Sealed = apigen.Some(apigen.SealedSecret{
			SmkVersion: c.u32("Secret", "smk_version", old.SmkVersion),
			Ciphertext: old.Ciphertext,
			Nonce:      old.Nonce,
		})
	}
	return checked("Secret", out, c.err)
}

func Config(old *apigenold.Config) (*apigen.Config, error) {
	c := &conv{}
	if old == nil {
		c.refuse("Config", "", "nil payload")
		return checked("Config", (*apigen.Config)(nil), c.err)
	}
	out := &apigen.Config{
		ID:      c.id("Config", "id", int64(old.ID)),
		SpaceID: c.id("Config", "space_id", int64(old.SpaceID)),
		Value:   old.Value,
	}
	if old.Fs == nil {
		c.refuse("Config", "fs", "unset")
	} else {
		out.Fs = apigen.ConfigFs{Key: old.Fs.Name, DirectoryID: c.optID("ConfigFs", "directory_id", int64(old.Fs.DirectoryID))}
	}
	return checked("Config", out, c.err)
}

func Asset(old *apigenold.Asset) (*apigen.Asset, error) {
	c := &conv{}
	if old == nil {
		c.refuse("Asset", "", "nil payload")
		return checked("Asset", (*apigen.Asset)(nil), c.err)
	}
	out := &apigen.Asset{
		ID:         c.id("Asset", "id", int64(old.ID)),
		SpaceID:    c.id("Asset", "space_id", int64(old.SpaceID)),
		StorageKey: old.StorageKey,
	}
	if old.Fs == nil {
		c.refuse("Asset", "fs", "unset")
	} else {
		out.Fs = apigen.AssetFs{Key: old.Fs.Key, DirectoryID: c.optID("AssetFs", "directory_id", int64(old.Fs.DirectoryID))}
	}
	if old.SizeBytes < 0 {
		c.refuse("Asset", "size_bytes", "negative size %d", old.SizeBytes)
	} else {
		out.SizeBytes = uint64(old.SizeBytes)
	}
	sum, err := hex.DecodeString(old.Sha256)
	if err != nil || len(sum) != 32 {
		c.refuse("Asset", "sha256", "%q is not a hex SHA-256 digest", old.Sha256)
	} else {
		out.Sha256 = sum
	}
	return checked("Asset", out, c.err)
}

func ValueDirectory(old *apigenold.ValueDirectory) (*apigen.ValueDirectory, error) {
	c := &conv{}
	if old == nil {
		c.refuse("ValueDirectory", "", "nil payload")
		return checked("ValueDirectory", (*apigen.ValueDirectory)(nil), c.err)
	}
	out := &apigen.ValueDirectory{
		ID:       c.id("ValueDirectory", "id", int64(old.ID)),
		SpaceID:  c.id("ValueDirectory", "space_id", int64(old.SpaceID)),
		Key:      old.Name,
		ParentID: c.optID("ValueDirectory", "parent_id", int64(old.ParentID)),
	}
	return checked("ValueDirectory", out, c.err)
}

func AssetDirectory(old *apigenold.AssetDirectory) (*apigen.AssetDirectory, error) {
	c := &conv{}
	if old == nil {
		c.refuse("AssetDirectory", "", "nil payload")
		return checked("AssetDirectory", (*apigen.AssetDirectory)(nil), c.err)
	}
	out := &apigen.AssetDirectory{
		ID:       c.id("AssetDirectory", "id", int64(old.ID)),
		SpaceID:  c.id("AssetDirectory", "space_id", int64(old.SpaceID)),
		Key:      old.Key,
		ParentID: c.optID("AssetDirectory", "parent_id", int64(old.ParentID)),
	}
	return checked("AssetDirectory", out, c.err)
}

// SecretKeyslot folds kind, node_id, and kdf_salt into the wrapping union
// under the fresh id the caller assigned (item 4). A salt on a machine slot or
// a node on a recovery slot is refused rather than dropped.
func SecretKeyslot(old *apigenold.SecretKeyslot, newID uint64) (*apigen.SecretKeyslot, error) {
	c := &conv{}
	if old == nil {
		c.refuse("SecretKeyslot", "", "nil payload")
		return checked("SecretKeyslot", (*apigen.SecretKeyslot)(nil), c.err)
	}
	out := &apigen.SecretKeyslot{
		ID:         newID,
		SmkVersion: c.u32("SecretKeyslot", "smk_version", old.SmkVersion),
		WrappedSmk: old.WrappedSmk,
		Nonce:      old.Nonce,
	}
	switch old.Kind {
	case apigenold.SecretKeyslotKind_SECRET_KEYSLOT_MACHINE:
		if len(old.KdfSalt) > 0 {
			c.refuse("SecretKeyslot", "kdf_salt", "set on a machine slot")
		}
		out.Wrapping = apigen.KeyslotWrapping{Value: apigen.KeyslotWrappingValueOneof{MachineKey: &apigen.MachineKey{
			NodeID: c.id("SecretKeyslot", "node_id", int64(old.NodeID)),
		}}}
	case apigenold.SecretKeyslotKind_SECRET_KEYSLOT_RECOVERY:
		if old.NodeID != 0 {
			c.refuse("SecretKeyslot", "node_id", "set on the recovery slot")
		}
		out.Wrapping = apigen.KeyslotWrapping{Value: apigen.KeyslotWrappingValueOneof{RecoveryCode: &apigen.RecoveryCode{KdfSalt: old.KdfSalt}}}
	default:
		c.refuse("SecretKeyslot", "kind", "unsupported value %d", old.Kind)
	}
	return checked("SecretKeyslot", out, c.err)
}
