package webuihandler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/pki"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
)

// Password login checks the user's own Argon2id hash. It is the default way
// in; ClusterSettings.Auth.RequirePasskey turns it off so only passkeys open
// a session. A wrong name and a wrong password fail the same way, and the
// hash is still verified against a decoy when the name is unknown so the
// response time does not reveal which.

var (
	PasskeyRequiredErr      = apigen.NewApiErr("Password sign-in is turned off; sign in with a passkey", "passkey_required", http.StatusForbidden)
	InvalidPasswordLoginErr = apigen.NewApiErr("Invalid username or password", "invalid_password_login", http.StatusUnauthorized)
	PasskeysUnavailableErr  = apigen.NewApiErr("Passkeys are unavailable on this server", "passkeys_unavailable", http.StatusServiceUnavailable)
)

func (h *Handler) requirePasskey() bool {
	settings := h.SystemConfig.Snapshot().Settings
	return h.SystemConfig.MustLoadBoolSetting(settings.Auth.RequirePasskey)
}

// localCAAvailable reports whether the Web UI is served under the locally
// generated CA, which is when offering the CA for download makes sense.
func (h *Handler) localCAAvailable() bool {
	if h.Secrets == nil {
		return false
	}
	settings := h.SystemConfig.Snapshot().Settings
	if !h.SystemConfig.MustLoadBoolSetting(settings.HttpsWeb.Enabled) ||
		!h.SystemConfig.MustLoadBoolSetting(settings.HttpsWeb.TlsSelfManaged) ||
		(settings.HttpsWeb.TlsCertPem.Present && settings.HttpsWeb.TlsCertPem.Value.Valid()) {
		return false
	}
	_, err := pki.LoadWebUILocalCA(h.Secrets)
	return err == nil
}

// GetV1AuthMethods is unauthenticated: the login page has to know which
// controls to render before anyone has a token.
func (h *Handler) GetV1AuthMethods(ctx apigen.Context) (*apigen.AuthMethodsResponse, error) {
	return &apigen.AuthMethodsResponse{
		PasskeyLoginEnabled: h.PasskeyService != nil,
		RequirePasskey:      h.requirePasskey(),
		LocalCaAvailable:    h.localCAAvailable(),
	}, nil
}

// decoyPasswordHash is verified against when the username is unknown, so an
// unknown name costs the same as a wrong password.
var decoyPasswordHash = func() string {
	hash, err := authu.HashPassword("decoy")
	if err != nil {
		panic(err)
	}
	return hash
}()

// PostV1AuthPasswordLogin signs the named user in with that user's password
// and opens an ordinary session; a pending user is activated by it.
func (h *Handler) PostV1AuthPasswordLogin(ctx apigen.Context, req *apigen.PasswordLoginRequest) (*apigen.LoginResponse, error) {
	if h.requirePasskey() {
		return nil, PasskeyRequiredErr
	}
	username := strings.TrimSpace(req.Username)
	if username == "" {
		return nil, UsernameRequiredErr
	}
	user, err := users.ByName(h.Store.Queries(), username)
	if err != nil && !errors.Is(err, users.ErrNotFound) {
		return nil, fmt.Errorf("looking up user: %w", err)
	}
	hash := decoyPasswordHash
	if user != nil && user.PasswordHash.Present {
		hash = user.PasswordHash.Value
	}
	ok, err := authu.VerifyPassword(req.Password, hash)
	if err != nil {
		return nil, fmt.Errorf("verifying password: %w", err)
	}
	if !ok || user == nil || !user.PasswordHash.Present {
		slog.InfoContext(ctx, "password login refused", "user", username)
		return nil, InvalidPasswordLoginErr
	}
	return h.startDefaultUserSession(ctx, user)
}

// GetV1TlsCaCert serves the local Web UI CA certificate as PEM so an operator
// can install it into a trust store. It is public material; it is offered only
// while the Web UI is actually served under that CA.
func (h *Handler) GetV1TlsCaCert(ctx apigen.Context, request *http.Request, writer http.ResponseWriter) error {
	if !h.localCAAvailable() {
		return apigen.NewApiErr("No local CA is in use", "local_ca_unavailable", http.StatusNotFound)
	}
	caCertPEM, err := pki.LoadWebUILocalCA(h.Secrets)
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
