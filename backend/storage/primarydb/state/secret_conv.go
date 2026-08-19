package state

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func secretVersionRecord(identity Secret, v pq.SecretVersion) secrets.Record {
	return secrets.Record{
		ID:         int32(v.ID),
		SecretID:   int32(identity.ID),
		Name:       identity.Name,
		Version:    int32(v.Version),
		SpaceID:    int32(identity.SpaceID),
		SMKVersion: int32(v.SmkVersion),
		Ciphertext: v.Ciphertext,
		Nonce:      v.Nonce,
		CreatedAt:  v.CreatedAt,
		Author:     int32(v.Author),
	}
}

// secretFromParts builds the wire shape: identity at the root, the space and
// version logs newest first. spaces and versions must be oldest first (query
// order); versions must be non-empty — a secret with no version is never
// surfaced.
func secretFromParts(sec Secret, spaces []pq.SecretSpace, versions []pq.ListSecretVersionMetasRow) *apigen.Secret {
	svs := make([]*apigen.SecretSpaceVersion, 0, len(spaces))
	for i := len(spaces) - 1; i >= 0; i-- {
		svs = append(svs, secretSpaceVersionFromRow(spaces[i]))
	}
	vs := make([]*apigen.SecretVersion, 0, len(versions))
	for i := len(versions) - 1; i >= 0; i-- {
		vs = append(vs, secretVersionFromRow(versions[i]))
	}
	return &apigen.Secret{
		ID: int32(sec.ID),
		Fs: &apigen.SecretFs{
			Name:        sec.Name,
			DirectoryID: int32(sec.ValueDirectoryID),
		},
		SpaceVersions: svs,
		Versions:      vs,
	}
}

func secretSpaceVersionFromRow(r pq.SecretSpace) *apigen.SecretSpaceVersion {
	return &apigen.SecretSpaceVersion{
		ID:        int32(r.ID),
		CreatedAt: time.UnixMilli(r.CreatedAt),
		Author:    int32(r.Author),
		SpaceID:   int32(r.SpaceID),
		GlobalSeq: r.GlobalSeq,
	}
}

func secretVersionFromRow(v pq.ListSecretVersionMetasRow) *apigen.SecretVersion {
	return &apigen.SecretVersion{
		ID:        int32(v.ID),
		Version:   int32(v.Version),
		CreatedAt: time.UnixMilli(v.CreatedAt),
		Author:    int32(v.Author),
		GlobalSeq: v.GlobalSeq,
	}
}
