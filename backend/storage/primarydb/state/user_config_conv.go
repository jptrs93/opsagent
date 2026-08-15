package state

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

const (
	eventTypeConfig   = int64(apigen.AuthzEntity_AUTHZ_ENTITY_CONFIG)
	eventActionCreate = int64(apigen.AuthzVerb_AUTHZ_VERB_CREATE)
	eventActionUpdate = int64(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)
	eventActionDelete = int64(apigen.AuthzVerb_AUTHZ_VERB_DELETE)
)

func configValueEvents(events []pq.Event) []pq.Event {
	out := make([]pq.Event, 0, len(events))
	for _, e := range events {
		if e.Action != eventActionDelete {
			out = append(out, e)
		}
	}
	return out
}

func configMetaFromDisplay(d pq.ConfigDisplay, valueEvents []pq.Event) *apigen.ConfigMeta {
	refs := make([]*apigen.ConfigVersionMeta, 0, len(valueEvents))
	for i, e := range valueEvents {
		refs = append([]*apigen.ConfigVersionMeta{{
			ID:        int32(e.ID),
			Version:   int32(i + 1),
			Value:     string(e.Blob),
			CreatedAt: time.UnixMilli(e.Ts),
			CreatedBy: int32(e.AuthorID),
		}}, refs...)
	}
	first := valueEvents[0]
	return &apigen.ConfigMeta{
		ID:               int32(d.ID),
		Name:             d.Name,
		SpaceID:          int32(d.SpaceID),
		ValueDirectoryID: int32(d.DirectoryID),
		CreatedAt:        time.UnixMilli(first.Ts),
		CreatedBy:        int32(first.AuthorID),
		VersionRefs:      refs,
	}
}
