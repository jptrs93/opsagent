package agentsessions

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func agentSessionRowToRecord(row pq.AgentSession) Record {
	return Record{
		ID:                row.SessionID,
		UserID:            int32(row.UserID),
		CreatedAt:         time.UnixMilli(row.CreatedAt),
		ExpiresAt:         timeOrZero(row.ExpiresAt),
		TokenHash:         row.TokenHash,
		TokenPrefix:       row.TokenPrefix,
		Status:            apigen.AgentSessionStatus(row.Status),
		RequestingAddress: row.RequestingAddress,
		ApprovalCode:      row.ApprovalCode,
		ApprovedAt:        timeOrZero(row.ApprovedAt),
	}
}

func ToProto(rec Record) *apigen.AgentSession {
	return &apigen.AgentSession{ID: rec.ID, UserID: rec.UserID, ExpiresAt: rec.ExpiresAt, TokenPrefix: rec.TokenPrefix, Status: rec.Status, RequestingAddress: rec.RequestingAddress, ApprovalCode: rec.ApprovalCode, ApprovedAt: rec.ApprovedAt}
}
