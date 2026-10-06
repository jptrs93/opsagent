package agentsessions

import (
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func agentSessionRowToRecord(row pq.AgentSession) Record {
	return Record{
		ID:                row.SessionID,
		UserID:            row.UserID,
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

// ToProto is the wire document without the token hash.
func ToProto(rec Record) *apigen.AgentSession {
	s := &apigen.AgentSession{SessionID: rec.ID, UserID: rec.UserID, Status: rec.Status, RequestingAddress: rec.RequestingAddress, ApprovedAt: apigen.TimeOf(rec.ApprovedAt)}
	if rec.ApprovalCode != "" {
		s.ApprovalCode = apigen.Some(rec.ApprovalCode)
	}
	if rec.Collected() {
		s.Token = apigen.Some(apigen.AgentToken{Prefix: rec.TokenPrefix, ExpiresAt: rec.ExpiresAt})
	}
	return s
}
