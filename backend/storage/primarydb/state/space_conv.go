package state

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func spaceRowToProto(row pq.Space) *apigen.Space {
	return &apigen.Space{ID: int32(row.ID), Name: row.Name}
}
