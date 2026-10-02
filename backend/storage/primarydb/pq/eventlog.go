package pq

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

// EventMeta is the envelope shared by every append-only entity table: the
// commit that wrote the row, the producer clock, the acting user, and whether
// the row creates, updates, or deletes its entity. Live state is the newest
// row per entity whose EventType is not delete.
type EventMeta struct {
	GlobalSeq int64
	EventTime int64
	Author    int64
	EventType apigen.AuthzVerb
}
