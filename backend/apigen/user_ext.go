package apigen

import (
	"encoding/json"
	"fmt"
	"log/slog"

	gowebauthn "github.com/go-webauthn/webauthn/webauthn"
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

func (m *InternalUser) WebAuthnCredentials() []gowebauthn.Credential {
	var res []gowebauthn.Credential
	for _, c := range m.Credentials {
		var out gowebauthn.Credential
		err := json.Unmarshal(c.Data, &out)
		if err != nil {
			slog.Warn(fmt.Sprintf("unmarshalling user %v gowebauthn.Credential: %v", m.ID, err))
		} else {
			res = append(res, out)
		}
	}
	return res
}
