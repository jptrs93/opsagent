package state

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// secretFromEvents builds the wire shape from one secret's full event list,
// oldest first (query order) and non-empty: identity from the latest row, the
// space and version logs newest first. Space entries are the space_changed
// events (the create event carries the initial assignment); version entries
// are the value_changed events. Never returns ciphertext.
func secretFromEvents(events []pq.SecretEventMeta) *apigen.Secret {
	latest := events[len(events)-1]
	svs := []*apigen.SecretSpaceVersion{}
	vs := []*apigen.SecretVersion{}
	for i := len(events) - 1; i >= 0; i-- {
		e := events[i]
		if e.SpaceChanged {
			svs = append(svs, secretSpaceVersionFromEvent(e))
		}
		if e.ValueChanged {
			vs = append(vs, secretVersionFromEvent(e))
		}
	}
	return &apigen.Secret{
		ID: int32(latest.SecretID),
		Fs: &apigen.SecretFs{
			Name:        latest.Name,
			DirectoryID: int32(latest.ValueDirectoryID),
		},
		SpaceVersions: svs,
		Versions:      vs,
	}
}

func secretSpaceVersionFromEvent(e pq.SecretEventMeta) *apigen.SecretSpaceVersion {
	return &apigen.SecretSpaceVersion{
		ID:        int32(e.ID),
		CreatedAt: time.UnixMilli(e.EventTime),
		Author:    int32(e.Author),
		SpaceID:   int32(e.SpaceID),
		GlobalSeq: e.GlobalSeq,
	}
}

func secretVersionFromEvent(e pq.SecretEventMeta) *apigen.SecretVersion {
	return &apigen.SecretVersion{
		ID:        int32(e.ID),
		Version:   int32(e.ValueVersion),
		CreatedAt: time.UnixMilli(e.EventTime),
		Author:    int32(e.Author),
		GlobalSeq: e.GlobalSeq,
	}
}

func secretVersionRecordRowToRecord(r pq.SecretVersionRecordRow) secrets.Record {
	return secrets.Record{
		ID:         int32(r.ID),
		SecretID:   int32(r.SecretID),
		Name:       r.Name,
		Version:    int32(r.Version),
		SpaceID:    int32(r.SpaceID),
		SMKVersion: int32(r.SmkVersion),
		Ciphertext: r.Ciphertext,
		Nonce:      r.Nonce,
		CreatedAt:  r.CreatedAt,
		Author:     int32(r.Author),
	}
}
