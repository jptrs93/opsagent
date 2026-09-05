package webuihandler

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"time"

	"github.com/go-webauthn/webauthn/protocol"
	"github.com/go-webauthn/webauthn/webauthn"
	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/ainit"
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
		b, marshalErr := json.Marshal(credential)
		if marshalErr != nil {
			return marshalErr
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

// webUIHosts are the hostnames the Web UI is reached by: the configured
// hosts setting (which doubles as the ACME host list when ACME is on), or
// localhost when none is configured.
func (h *Handler) webUIHosts() []string {
	hostsValue := h.ConfigService.MustLoadConfigStringValue(h.Config.HttpsWeb.AcmeHosts)
	var hosts []string
	for _, host := range stringu.ParseStringList(hostsValue) {
		if host = strings.TrimSpace(host); host != "" {
			hosts = append(hosts, host)
		}
	}
	if len(hosts) == 0 {
		return []string{"localhost"}
	}
	return hosts
}

// passkeyRPID is the first configured hostname that is a DNS name. WebAuthn
// forbids IP addresses as relying-party ids, so an install reached only by
// address falls back to localhost. Over plain HTTP browsers only ever run
// WebAuthn for localhost, so HTTP-only mode uses that regardless of the
// configured hostnames (older installs still carry the ACME placeholder).
func (h *Handler) passkeyRPID() (string, error) {
	if h.httpOnly() {
		return "localhost", nil
	}
	for _, host := range h.webUIHosts() {
		if net.ParseIP(host) == nil {
			return host, nil
		}
	}
	return "localhost", nil
}

// passkeyOrigins lists every origin the Web UI answers on: each hostname
// under each enabled scheme, with the listen port when it is not the scheme
// default, plus the Vite dev server in HTTP-only mode and any explicit extras.
func (h *Handler) passkeyOrigins() ([]string, error) {
	httpEnabled := h.ConfigService.MustLoadConfigBoolValue(h.Config.HttpWeb.Enabled)
	httpsEnabled := h.ConfigService.MustLoadConfigBoolValue(h.Config.HttpsWeb.Enabled)
	httpPort := listenPortOrDefault(h.ConfigService.MustLoadConfigStringValue(h.Config.HttpWeb.Listen), "80")
	httpsPort := listenPortOrDefault(h.ConfigService.MustLoadConfigStringValue(h.Config.HttpsWeb.Listen), "443")
	var origins []string
	add := func(origin string) {
		if !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	for _, host := range h.webUIHosts() {
		if httpsEnabled {
			add(originFor("https", host, httpsPort, "443"))
		}
		if httpEnabled {
			add(originFor("http", host, httpPort, "80"))
		}
	}
	if httpEnabled && !httpsEnabled {
		// The RP id is localhost here, so make sure its origin is listed even
		// when the hostnames setting says something else; and the Vite dev
		// server for local frontend work.
		add(originFor("http", "localhost", httpPort, "80"))
		add("http://localhost:5173")
	}
	for _, origin := range ainit.StaticConfig.PasskeyExtraOrigins {
		if origin = strings.TrimSpace(origin); origin != "" {
			add(origin)
		}
	}
	return origins, nil
}

func (h *Handler) httpOnly() bool {
	return h.ConfigService.MustLoadConfigBoolValue(h.Config.HttpWeb.Enabled) &&
		!h.ConfigService.MustLoadConfigBoolValue(h.Config.HttpsWeb.Enabled)
}

func originFor(scheme, host, port, defaultPort string) string {
	if strings.Contains(host, ":") && !strings.HasPrefix(host, "[") {
		host = "[" + host + "]"
	}
	if port == defaultPort {
		return scheme + "://" + host
	}
	return scheme + "://" + host + ":" + port
}

func listenPortOrDefault(listen, fallback string) string {
	_, port, err := net.SplitHostPort(strings.TrimSpace(listen))
	if err != nil || port == "" {
		return fallback
	}
	return port
}

func (h *Handler) PostV1AuthPasskeyRegisterStart(ctx apigen.Context) (*apigen.WebAuthNOptionsResponse, error) {
	if err := requireHuman(ctx); err != nil {
		return nil, err
	}
	if h.PasskeyService == nil {
		return nil, PasskeysUnavailableErr
	}
	sessionID, optionsJSON, err := h.PasskeyService.BeginRegistration(ctx.User.WebAuthNID)
	if err != nil {
		return nil, apigen.NewApiErr("bad credentials", fmt.Sprintf("err=%v", err), http.StatusBadRequest)
	}
	return &apigen.WebAuthNOptionsResponse{SessionID: sessionID, OptionsJson: optionsJSON}, nil
}

func (h *Handler) PostV1AuthPasskeyRegisterFinish(ctx apigen.Context, req *apigen.WebAuthNFinishRequest) (*apigen.LoginResponse, error) {
	if err := requireHuman(ctx); err != nil {
		return nil, err
	}
	if h.PasskeyService == nil {
		return nil, PasskeysUnavailableErr
	}
	_, err := h.PasskeyService.FinishRegistration(ctx.User.WebAuthNID, req.SessionID, req.CredentialJson)
	if err != nil {
		return nil, apigen.NewApiErr("bad credentials", fmt.Sprintf("err=%v", err), http.StatusBadRequest)
	}
	// Registration ends with a full session, so it counts as a login too.
	return h.startPersonalSession(ctx, ctx.User)
}

func (h *Handler) PostV1AuthPasskeyLoginStart(ctx apigen.Context) (*apigen.WebAuthNOptionsResponse, error) {
	if h.PasskeyService == nil {
		return nil, PasskeysUnavailableErr
	}
	sessionID, optionsJSON, err := h.PasskeyService.BeginLogin()
	if err != nil {
		return nil, apigen.NewApiErr("bad credentials", fmt.Sprintf("err=%v", err), http.StatusBadRequest)
	}
	return &apigen.WebAuthNOptionsResponse{SessionID: sessionID, OptionsJson: optionsJSON}, nil
}

func (h *Handler) PostV1AuthPasskeyLoginFinish(ctx apigen.Context, req *apigen.WebAuthNFinishRequest) (*apigen.LoginResponse, error) {
	if h.PasskeyService == nil {
		return nil, PasskeysUnavailableErr
	}
	user, err := h.PasskeyService.FinishLogin(req.SessionID, req.CredentialJson)
	if err != nil {
		return nil, apigen.NewApiErr("bad credentials", fmt.Sprintf("err=%v", err), http.StatusBadRequest)
	}
	return h.startPersonalSession(ctx, user)
}
