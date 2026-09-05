package webuihandler

import (
	"errors"
	"testing"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/authz"
	"github.com/jptrs93/opsagent/backend/lib/config"
)

const testMasterPassword = "opendeploy-test-master-password"

func enablePasswordLogin(t *testing.T, h *Handler) {
	t.Helper()
	settings := config.DefaultSettings(config.DefaultInitialConfig())
	settings.Auth.PasswordLoginEnabled = apigen.BoolSetting{Value: true}
	if err := h.ConfigService.UpdateSettings(*settings); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
	hash, err := authu.HashPassword(testMasterPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := h.ConfigService.SetMasterPasswordHash(hash); err != nil {
		t.Fatalf("SetMasterPasswordHash: %v", err)
	}
	authzService, err := authz.Open(h.Store)
	if err != nil {
		t.Fatalf("authz.Open: %v", err)
	}
	h.Authz = authzService
}

func TestPasswordLoginDisabledByDefault(t *testing.T) {
	h, user := newAuthTestHandler(t)
	methods, err := h.GetV1AuthMethods(apigen.Context{})
	if err != nil {
		t.Fatalf("GetV1AuthMethods: %v", err)
	}
	if methods.PasswordLoginEnabled || methods.LocalCaAvailable {
		t.Fatalf("methods = %+v, want password login off and no local CA", methods)
	}
	_, err = h.PostV1AuthPasswordLogin(apigen.Context{}, &apigen.PasswordLoginRequest{Username: user.Name, Password: testMasterPassword})
	if !errors.Is(err, PasswordLoginDisabledErr) {
		t.Fatalf("login err = %v, want PasswordLoginDisabledErr", err)
	}
}

func TestPasswordLoginUsesMasterPasswordAndCreatesUsers(t *testing.T) {
	h, user := newAuthTestHandler(t)
	enablePasswordLogin(t, h)

	// Existing user, padded stored name, padded input: both sides trim.
	h.Store.UpdateUserMatching(func(u *apigen.InternalUser) bool { return u.ID == user.ID }, func(u *apigen.InternalUser) {
		u.Name = " operator "
	})
	res, err := h.PostV1AuthPasswordLogin(apigen.Context{}, &apigen.PasswordLoginRequest{Username: " operator ", Password: testMasterPassword})
	if err != nil {
		t.Fatalf("PostV1AuthPasswordLogin: %v", err)
	}
	if res.UserID != user.ID || len(res.Scopes) != 1 || res.Scopes[0] != ScopeDefault {
		t.Fatalf("login response = %+v, want user %d with [default]", res, user.ID)
	}
	if _, _, err := h.jwtAuth.VerifyAndResolveUser(res.Token); err != nil {
		t.Fatalf("issued token does not verify: %v", err)
	}
	if sessions, _ := h.Store.ListPersonalSessionsForUser(user.ID); len(sessions) != 1 {
		t.Fatalf("personal sessions = %d, want 1", len(sessions))
	}

	// Unknown user is created with cluster_admin, as first-time setup does.
	res, err = h.PostV1AuthPasswordLogin(apigen.Context{}, &apigen.PasswordLoginRequest{Username: "newcomer", Password: testMasterPassword})
	if err != nil {
		t.Fatalf("PostV1AuthPasswordLogin (new user): %v", err)
	}
	created, err := h.Store.FetchUserByID(res.UserID)
	if err != nil || created.Name != "newcomer" {
		t.Fatalf("created user = %+v, err %v", created, err)
	}
	grants := h.Authz.GrantsForUser(int64(created.ID))
	if len(grants) != 1 || grants[0].TemplateID != authz.ClusterAdminTemplateID {
		t.Fatalf("grants for new user = %+v; want one cluster_admin grant", grants)
	}

	for _, tc := range []struct {
		name, user, password string
		want                 error
	}{
		{"wrong password", "operator", "not the password", InvalidPasswordLoginErr},
		{"empty password", "operator", "", InvalidPasswordLoginErr},
		{"empty username", "", testMasterPassword, UsernameRequiredErr},
	} {
		_, err := h.PostV1AuthPasswordLogin(apigen.Context{}, &apigen.PasswordLoginRequest{Username: tc.user, Password: tc.password})
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
}

func TestPasskeyEndpointsReportUnavailableWithoutService(t *testing.T) {
	h, user := newAuthTestHandler(t)
	methods, err := h.GetV1AuthMethods(apigen.Context{})
	if err != nil || methods.PasskeyLoginEnabled {
		t.Fatalf("methods = %+v, err %v; want passkeys reported unavailable when no service is wired", methods, err)
	}
	if _, err := h.PostV1AuthPasskeyLoginStart(apigen.Context{}); !errors.Is(err, PasskeysUnavailableErr) {
		t.Fatalf("login start err = %v, want PasskeysUnavailableErr", err)
	}
	if _, err := h.PostV1AuthPasskeyRegisterStart(apigen.Context{User: user}); !errors.Is(err, PasskeysUnavailableErr) {
		t.Fatalf("register start err = %v, want PasskeysUnavailableErr", err)
	}
}
