package apigen

import (
	"context"
	"encoding/json"
	"log/slog"

	"github.com/go-webauthn/webauthn/webauthn"
)

func (m *User) WebAuthnID() []byte {
	return m.Authentication.WebAuthnID
}

func (m *User) WebAuthnName() string {
	return m.Name
}

func (m *User) WebAuthnDisplayName() string {
	return m.Name
}

func (m *User) WebAuthnCredentials() []webauthn.Credential {
	var res []webauthn.Credential
	for _, c := range m.Authentication.Credentials {
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
