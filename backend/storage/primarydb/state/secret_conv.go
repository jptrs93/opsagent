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
		CreatedBy:  int32(v.CreatedBy),
	}
}

func secretMetaFromRow(sec Secret, refs []*apigen.SecretVersionMeta) *apigen.SecretMeta {
	return &apigen.SecretMeta{
		ID:               int32(sec.ID),
		Name:             sec.Name,
		SpaceID:          int32(sec.SpaceID),
		ValueDirectoryID: int32(sec.ValueDirectoryID),
		CreatedAt:        time.UnixMilli(sec.CreatedAt),
		CreatedBy:        int32(sec.CreatedBy),
		VersionRefs:      refs,
	}
}

func secretVersionMetaFromRow(v pq.ListSecretVersionMetasRow) *apigen.SecretVersionMeta {
	return &apigen.SecretVersionMeta{
		ID:        int32(v.ID),
		Version:   int32(v.Version),
		CreatedAt: time.UnixMilli(v.CreatedAt),
		CreatedBy: int32(v.CreatedBy),
	}
}
