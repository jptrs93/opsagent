package legacyconv

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
)

func Space(old *apigenold.Space) (*apigen.Space, error) {
	c := &conv{}
	if old == nil {
		c.refuse("Space", "", "nil payload")
		return checked("Space", (*apigen.Space)(nil), c.err)
	}
	return checked("Space", &apigen.Space{ID: c.id("Space", "id", int64(old.ID)), Name: old.Name}, c.err)
}

// User lifts the InternalUser blob nested in the old credentials field into
// UserAuthentication. The name comes from the entity and falls back to the
// nested copy when the entity's is empty.
func User(old *apigenold.User) (*apigen.User, error) {
	c := &conv{}
	if old == nil {
		c.refuse("User", "", "nil payload")
		return checked("User", (*apigen.User)(nil), c.err)
	}
	internal, err := apigenold.DecodeInternalUser(old.Credentials)
	if err != nil {
		c.refuse("User", "credentials", "decode InternalUser: %v", err)
		return checked("User", (*apigen.User)(nil), c.err)
	}
	name := old.Name
	if name == "" {
		name = internal.Name
	}
	out := &apigen.User{
		ID:             c.id("User", "id", int64(old.ID)),
		Name:           name,
		Authentication: apigen.UserAuthentication{WebAuthnID: internal.WebAuthNID},
	}
	for _, cred := range internal.Credentials {
		if cred == nil {
			c.refuse("InternalUser", "credentials", "nil entry")
			continue
		}
		out.Authentication.Credentials = append(out.Authentication.Credentials, apigen.WebAuthnCredential{ID: cred.ID, Data: cred.Data})
	}
	return checked("User", out, c.err)
}

// AgentSession takes the log row's entity id as the new integer id and keeps
// the old string id as session_id. The token is present when the old row had
// collected one: the hash, prefix, and expiry are written together at pickup
// and a pending request carries none of them.
func AgentSession(old *apigenold.AgentSession, id uint64) (*apigen.AgentSession, error) {
	c := &conv{}
	if old == nil {
		c.refuse("AgentSession", "", "nil payload")
		return checked("AgentSession", (*apigen.AgentSession)(nil), c.err)
	}
	out := &apigen.AgentSession{
		ID:                id,
		SessionID:         old.ID,
		UserID:            c.id("AgentSession", "user_id", int64(old.UserID)),
		Status:            apigen.AgentSessionStatus(old.Status),
		ApprovedAt:        apigen.TimeOf(old.ApprovedAt),
		RequestingAddress: old.RequestingAddress,
		ApprovalCode:      optString(old.ApprovalCode),
	}
	if len(old.TokenHash) > 0 || old.TokenPrefix != "" || !old.ExpiresAt.IsZero() {
		out.Token = apigen.Some(apigen.AgentToken{Hash: optBytes(old.TokenHash), Prefix: old.TokenPrefix, ExpiresAt: old.ExpiresAt})
	}
	return checked("AgentSession", out, c.err)
}

func UserSession(old *apigenold.UserSession, id uint64) (*apigen.UserSession, error) {
	c := &conv{}
	if old == nil {
		c.refuse("UserSession", "", "nil payload")
		return checked("UserSession", (*apigen.UserSession)(nil), c.err)
	}
	out := &apigen.UserSession{
		ID:                id,
		SessionID:         old.ID,
		UserID:            c.id("UserSession", "user_id", int64(old.UserID)),
		Kind:              apigen.UserSessionKind(old.Kind),
		ExpiresAt:         old.ExpiresAt,
		RevokedAt:         apigen.TimeOf(old.RevokedAt),
		RequestingAddress: old.RequestingAddress,
		UserAgent:         old.UserAgent,
		TokenHash:         optBytes(old.TokenHash),
	}
	return checked("UserSession", out, c.err)
}

// NixStoreReset drops the old requested_at: the reducer takes the request
// time from the write's clock, which the caller keeps from the log row.
func NixStoreReset(old *apigenold.NixStoreReset, id uint64) (*apigen.NixStoreReset, error) {
	c := &conv{}
	if old == nil {
		c.refuse("NixStoreReset", "", "nil payload")
		return checked("NixStoreReset", (*apigen.NixStoreReset)(nil), c.err)
	}
	return checked("NixStoreReset", &apigen.NixStoreReset{ID: id, Repo: old.Repo}, c.err)
}
