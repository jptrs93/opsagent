package webuihandler

import (
	"errors"
	"net/http"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

// Password login lets the cluster master password open a full session
// directly, instead of only minting a bootstrap token for passkey enrolment.
// It exists for installs where a browser will not run WebAuthn at all: plain
// HTTP on anything other than localhost, or HTTPS behind a certificate the
// browser does not trust. It is gated by
// ClusterSettings.Auth.PasswordLoginEnabled so a production install that never
// turns it on exposes no password login surface.

var (
	PasswordLoginDisabledErr = apigen.NewApiErr("Password login is not enabled", "password_login_disabled", http.StatusForbidden)
	InvalidPasswordLoginErr  = apigen.NewApiErr("Invalid master password", "invalid_master_password", http.StatusUnauthorized)
	PasskeysUnavailableErr   = apigen.NewApiErr("Passkeys are unavailable on this server", "passkeys_unavailable", http.StatusServiceUnavailable)
)

func (h *Handler) passwordLoginEnabled() bool {
	settings := h.ConfigService.Snapshot().Settings
	return h.ConfigService.MustLoadConfigBoolValue(settings.Auth.PasswordLoginEnabled)
}

// localCAAvailable reports whether the Web UI is served under the locally
// generated CA, which is when offering the CA for download makes sense.
func (h *Handler) localCAAvailable() bool {
	if h.Secrets == nil {
		return false
	}
	settings := h.ConfigService.Snapshot().Settings
	if !h.ConfigService.MustLoadConfigBoolValue(settings.HttpsWeb.Enabled) ||
		!h.ConfigService.MustLoadConfigBoolValue(settings.HttpsWeb.TlsSelfManaged) ||
		settings.HttpsWeb.TlsCertPem.VersionID != 0 {
		return false
	}
	_, err := certu.LoadWebUILocalCA(h.Secrets)
	return err == nil
}

// GetV1AuthMethods is unauthenticated: the login page has to know which
// controls to render before anyone has a token.
func (h *Handler) GetV1AuthMethods(ctx apigen.Context) (*apigen.AuthMethodsResponse, error) {
	return &apigen.AuthMethodsResponse{
		PasskeyLoginEnabled:  h.PasskeyService != nil,
		PasswordLoginEnabled: h.passwordLoginEnabled(),
		LocalCaAvailable:     h.localCAAvailable(),
	}, nil
}

// PostV1AuthPasswordLogin signs the named user in with the master password.
// The user is created on first use with the same defaults as first-time
// setup, and the result is an ordinary personal session.
func (h *Handler) PostV1AuthPasswordLogin(ctx apigen.Context, req *apigen.PasswordLoginRequest) (*apigen.LoginResponse, error) {
	if !h.passwordLoginEnabled() {
		return nil, PasswordLoginDisabledErr
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		return nil, UsernameRequiredErr
	}
	if err := h.verifyMasterPassword(req.Password); err != nil {
		if errors.Is(err, InvalidMasterPasswordErr) {
			return nil, InvalidPasswordLoginErr
		}
		return nil, err
	}
	user, err := h.resolveOrCreateUser(username)
	if err != nil {
		return nil, err
	}
	return h.startPersonalSession(ctx, user)
}

// GetV1TlsCaCert serves the local Web UI CA certificate as PEM so an operator
// can install it into a trust store. It is public material; it is offered only
// while the Web UI is actually served under that CA.
func (h *Handler) GetV1TlsCaCert(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	if !h.localCAAvailable() {
		return apigen.NewApiErr("No local CA is in use", "local_ca_unavailable", http.StatusNotFound)
	}
	caCertPEM, err := certu.LoadWebUILocalCA(h.Secrets)
	if err != nil {
		if errors.Is(err, secrets.ErrNotFound) {
			return apigen.NewApiErr("No local CA is in use", "local_ca_unavailable", http.StatusNotFound)
		}
		return err
	}
	writer.Header().Set("Content-Type", "application/x-pem-file")
	writer.Header().Set("Content-Disposition", `attachment; filename="opendeploy-ca.crt"`)
	writer.Header().Set("Cache-Control", "no-store")
	_, err = writer.Write(caCertPEM)
	return err
}
