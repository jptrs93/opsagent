package apigen

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/go-webauthn/webauthn/webauthn"
)

func (m *InternalUser) WebAuthnID() []byte {
	return m.WebAuthNID
}

func (m *InternalUser) WebAuthnName() string {
	return m.Name
}

func (m *InternalUser) WebAuthnDisplayName() string {
	return m.Name
}

func (m *InternalUser) WebAuthnCredentials() []webauthn.Credential {
	var res []webauthn.Credential
	for _, c := range m.Credentials {
		var out webauthn.Credential
		err := json.Unmarshal(c.Data, &out)
		if err != nil {
			slog.WarnContext(context.Background(), "unmarshalling webauthn.Credential failed", "user", m.ID, "err", err)
		} else {
			res = append(res, out)
		}
	}
	return res
}
