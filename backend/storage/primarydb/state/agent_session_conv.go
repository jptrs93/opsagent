package state

import (
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func agentSessionRowToRecord(row pq.AgentSession) AgentSessionRecord {
	rec := AgentSessionRecord{
		ID:                row.ID,
		UserID:            int32(row.UserID),
		CreatedAt:         time.Unix(row.CreatedAt, 0),
		ExpiresAt:         timeOrZero(row.ExpiresAt),
		TokenHash:         row.TokenHash,
		TokenPrefix:       row.TokenPrefix,
		RevokedAt:         timeOrZero(row.RevokedAt),
		Status:            apigen.AgentSessionStatus(row.Status),
		RequestingAddress: row.RequestingAddress,
		ApprovalCode:      row.ApprovalCode,
		ApprovedAt:        timeOrZero(row.ApprovedAt),
	}
	if row.Scopes != "" {
		rec.Scopes = strings.Split(row.Scopes, ",")
	}
	return rec
}
