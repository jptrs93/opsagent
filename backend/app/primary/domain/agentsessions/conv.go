package agentsessions

import (
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func agentSessionRowToRecord(row pq.AgentSession) Record {
	rec := Record{
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

func ToProto(rec Record) *apigen.AgentSession {
	return &apigen.AgentSession{ID: rec.ID, UserID: rec.UserID, CreatedAt: rec.CreatedAt, ExpiresAt: rec.ExpiresAt, TokenPrefix: rec.TokenPrefix, Scopes: append([]string(nil), rec.Scopes...), Status: rec.Status, RequestingAddress: rec.RequestingAddress, ApprovalCode: rec.ApprovalCode, ApprovedAt: rec.ApprovedAt}
}
