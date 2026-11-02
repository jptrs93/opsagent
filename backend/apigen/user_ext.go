package apigen

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/go-webauthn/webauthn/webauthn"
)

func (m *User) WebAuthnID() []byte {
	return m.Authentication.WebAuthnID
}

// DisplayName is the name shown for the account: the chosen name, or a
// placeholder built from the id while an invitee has not picked one yet.
func (m *User) DisplayName() string {
	if m.Name.Present {
		return m.Name.Value
	}
	return fmt.Sprintf("user-%d", m.ID)
}

func (m *User) WebAuthnName() string {
	return m.DisplayName()
}

func (m *User) WebAuthnDisplayName() string {
	return m.DisplayName()
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
