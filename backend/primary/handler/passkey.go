package handler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/util/stringu"
)

const defaultSessionTokenTTL = 2 * 24 * time.Hour

func credentialIDMatcher(credentialID []byte) func(user *apigen.InternalUser) bool {
	return func(u *apigen.InternalUser) bool {
		for _, j := range u.Credentials {
			if bytes.Equal(j.ID, credentialID) {
				return true
			}
		}
		return false
	}
}
func userIDMatcher(userWebAuthnID []byte) func(user *apigen.InternalUser) bool {
	return func(user *apigen.InternalUser) bool { return bytes.Equal(user.WebAuthNID, userWebAuthnID) }
}

func (h *Handler) initPasskeyService() error {

	rpID, err := h.passkeyRPID()
	if err != nil {
		return err
	}
	origins, err := h.passkeyOrigins()
	if err != nil {
		return err
	}
	service, err := authu.NewPasskeyService[*apigen.InternalUser](&webauthn.Config{
		RPDisplayName: "Opsagent",
		RPID:          rpID,
		RPOrigins:     origins,
		AuthenticatorSelection: protocol.AuthenticatorSelection{
			ResidentKey:      protocol.ResidentKeyRequirementRequired,
			UserVerification: protocol.VerificationRequired,
		},
		Timeouts: webauthn.TimeoutsConfig{
			Login:        webauthn.TimeoutConfig{Enforce: true, Timeout: 5 * time.Minute},
			Registration: webauthn.TimeoutConfig{Enforce: true, Timeout: 5 * time.Minute},
		},
	}, func(userID []byte, credential *webauthn.Credential) error {
		b, err := json.Marshal(credential)
		if err != nil {
			return err
		}
		// The userID was just produced by an in-flight registration session, so
		// the user must exist. Storage failure → crash; supervisor restarts.
		h.Store.UpdateUserMatching(userIDMatcher(userID), func(d *apigen.InternalUser) {
			d.Credentials = append(d.Credentials, &apigen.WebAuthnCredential{
				ID:   credential.ID,
				Data: b,
			})
		})
		return nil
	}, func(userID []byte) (*apigen.InternalUser, error) {
		return h.Store.FetchUserMatching(userIDMatcher(userID))
	}, func(credentialID []byte) (*apigen.InternalUser, error) {
		return h.Store.FetchUserMatching(credentialIDMatcher(credentialID))
	})
	if err != nil {
		return err
	}
	h.PasskeyService = service
	return nil
}

func (h *Handler) passkeyRPID() (string, error) {
	httpEnabled := h.ConfigService.MustLoadConfigBoolValue(h.Config.HttpWeb.Enabled)
	httpsEnabled := h.ConfigService.MustLoadConfigBoolValue(h.Config.HttpsWeb.Enabled)
	if httpEnabled && !httpsEnabled {
		return "localhost", nil
	}
	hostsValue := h.ConfigService.MustLoadConfigStringValue(h.Config.HttpsWeb.AcmeHosts)
	hosts := stringu.ParseStringList(hostsValue)
	if len(hosts) == 0 || strings.TrimSpace(hosts[0]) == "" {
		return "opendeploy.dev", nil
	}
	return strings.TrimSpace(hosts[0]), nil
}

func (h *Handler) passkeyOrigins() ([]string, error) {
	httpEnabled := h.ConfigService.MustLoadConfigBoolValue(h.Config.HttpWeb.Enabled)
	httpsEnabled := h.ConfigService.MustLoadConfigBoolValue(h.Config.HttpsWeb.Enabled)
	if httpEnabled && !httpsEnabled {
		return []string{"http://localhost:8080", "http://localhost:5173"}, nil
	}
	hostsValue := h.ConfigService.MustLoadConfigStringValue(h.Config.HttpsWeb.AcmeHosts)
	hosts := stringu.ParseStringList(hostsValue)
	origins := make([]string, 0, len(hosts))
	for _, host := range hosts {
		host = strings.TrimSpace(host)
		if host == "" {
			continue
		}
		origins = append(origins, "https://"+host)
	}
	if len(origins) == 0 {
		return []string{"https://opendeploy.dev"}, nil
	}
	return origins, nil
}

func (h *Handler) PostV1AuthPasskeyRegisterStart(ctx apigen.Context, _ *apigen.EmptyRequest) (*apigen.WebAuthNOptionsResponse, error) {
	sessionID, optionsJSON, err := h.PasskeyService.BeginRegistration(ctx.User.WebAuthNID)
	if err != nil {
		return nil, apigen.NewApiErr("bad credentials", fmt.Sprintf("err=%v", err), http.StatusBadRequest)
	}
	return &apigen.WebAuthNOptionsResponse{SessionID: sessionID, OptionsJson: optionsJSON}, nil
}

func (h *Handler) PostV1AuthPasskeyRegisterFinish(ctx apigen.Context, req *apigen.WebAuthNFinishRequest) (*apigen.LoginResponse, error) {
	_, err := h.PasskeyService.FinishRegistration(ctx.User.WebAuthNID, req.SessionID, req.CredentialJson)
	if err != nil {
		return nil, apigen.NewApiErr("bad credentials", fmt.Sprintf("err=%v", err), http.StatusBadRequest)
	}
	expiry := time.Now().Add(defaultSessionTokenTTL)
	token, err := h.jwtAuth.GenerateTokenWith(ctx.User.ID, []string{"default"}, defaultSessionTokenTTL)
	if err != nil {
		return nil, err
	}
	return newLoginResponse(ctx.User, token, []string{"default"}, expiry), nil
}

func (h *Handler) PostV1AuthPasskeyLoginStart(ctx apigen.Context, _ *apigen.EmptyRequest) (*apigen.WebAuthNOptionsResponse, error) {
	sessionID, optionsJSON, err := h.PasskeyService.BeginLogin()
	if err != nil {
		return nil, apigen.NewApiErr("bad credentials", fmt.Sprintf("err=%v", err), http.StatusBadRequest)
	}
	return &apigen.WebAuthNOptionsResponse{SessionID: sessionID, OptionsJson: optionsJSON}, nil
}

func (h *Handler) PostV1AuthPasskeyLoginFinish(ctx apigen.Context, req *apigen.WebAuthNFinishRequest) (*apigen.LoginResponse, error) {
	user, err := h.PasskeyService.FinishLogin(req.SessionID, req.CredentialJson)
	if err != nil {
		return nil, apigen.NewApiErr("bad credentials", fmt.Sprintf("err=%v", err), http.StatusBadRequest)
	}
	expiry := time.Now().Add(defaultSessionTokenTTL)
	token, err := h.jwtAuth.GenerateTokenWith(user.ID, []string{"default"}, defaultSessionTokenTTL)
	if err != nil {
		return nil, err
	}
	return newLoginResponse(user, token, []string{"default"}, expiry), nil
}
