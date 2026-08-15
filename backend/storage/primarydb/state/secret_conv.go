package state

import (
	"fmt"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const eventTypeSecret = int64(apigen.AuthzEntity_AUTHZ_ENTITY_SECRET)

func secretRecordFromEvent(d pq.SecretDisplay, e pq.Event, ordinal int) secrets.Record {
	env, err := apigen.DecodeSecretEnvelope(e.Blob)
	if err != nil {
		panic(fmt.Sprintf("decode secret envelope for event %d: %v", e.ID, err))
	}
	return secrets.Record{
		ID:            int32(e.ID),
		SecretID:      int32(d.ID),
		Name:          d.Name,
		Version:       int32(ordinal),
		SpaceID:       int32(d.SpaceID),
		SMKVersion:    env.SmkVersion,
		Ciphertext:    env.Ciphertext,
		Nonce:         env.Nonce,
		LegacyVersion: env.LegacyVersion,
		CreatedAt:     e.Ts,
		CreatedBy:     int32(e.AuthorID),
	}
}

func secretMetaFromDisplay(d pq.SecretDisplay, valueEvents []pq.Event) *apigen.SecretMeta {
	refs := make([]*apigen.SecretVersionMeta, 0, len(valueEvents))
	for i, e := range valueEvents {
		refs = append([]*apigen.SecretVersionMeta{{
			ID:        int32(e.ID),
			Version:   int32(i + 1),
			CreatedAt: time.UnixMilli(e.Ts),
			CreatedBy: int32(e.AuthorID),
		}}, refs...)
	}
	first := valueEvents[0]
	return &apigen.SecretMeta{
		ID:               int32(d.ID),
		Name:             d.Name,
		SpaceID:          int32(d.SpaceID),
		ValueDirectoryID: int32(d.DirectoryID),
		CreatedAt:        time.UnixMilli(first.Ts),
		CreatedBy:        int32(first.AuthorID),
		VersionRefs:      refs,
	}
}
