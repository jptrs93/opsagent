package webuihandler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
)

func enablePasswordLogin(t *testing.T, h *Handler) {
	t.Helper()
	settings := config.DefaultSettings(config.DefaultInitialConfig())
	settings.Auth.PasswordLoginEnabled = apigen.BoolSetting{Value: true}
	if err := h.ConfigService.UpdateSettings(*settings); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
}

func TestPasswordLoginDisabledByDefault(t *testing.T) {
	h, user := newAuthTestHandler(t)
	methods, err := h.GetV1AuthMethods(apigen.Context{})
	if err != nil {
		t.Fatalf("GetV1AuthMethods: %v", err)
	}
	if methods.PasswordLoginEnabled || !methods.PasskeyLoginEnabled {
		t.Fatalf("methods = %+v, want passkey only", methods)
	}
	_, err = h.PostV1AuthPasswordLogin(apigen.Context{}, &apigen.PasswordLoginRequest{Username: user.Name, Password: "irrelevant"})
	if !errors.Is(err, PasswordLoginDisabledErr) {
		t.Fatalf("login err = %v, want PasswordLoginDisabledErr", err)
	}
	_, err = h.PostV1AuthPasswordSet(apigen.Context{User: user}, &apigen.PasswordSetRequest{Password: "correct horse battery"})
	if !errors.Is(err, PasswordLoginDisabledErr) {
		t.Fatalf("set err = %v, want PasswordLoginDisabledErr", err)
	}
}

func TestPasswordSetThenLoginIssuesDefaultSession(t *testing.T) {
	h, user := newAuthTestHandler(t)
	enablePasswordLogin(t, h)

	bootstrapUser := *user
	setRes, err := h.PostV1AuthPasswordSet(apigen.Context{User: &bootstrapUser}, &apigen.PasswordSetRequest{Password: "correct horse battery"})
	if err != nil {
		t.Fatalf("PostV1AuthPasswordSet: %v", err)
	}
	if len(setRes.Scopes) != 1 || setRes.Scopes[0] != ScopeDefault {
		t.Fatalf("set scopes = %v, want [default]", setRes.Scopes)
	}
	stored, err := h.Store.FetchUserByID(user.ID)
	if err != nil {
		t.Fatalf("FetchUserByID: %v", err)
	}
	if stored.PasswordHash == "" || strings.Contains(stored.PasswordHash, "correct horse") {
		t.Fatalf("stored hash %q is empty or holds the plaintext", stored.PasswordHash)
	}

	// A user created before names were trimmed may carry padding; both the
	// padded stored name and the padded login input must still match.
	h.Store.UpdateUserMatching(func(u *apigen.InternalUser) bool { return u.ID == user.ID }, func(u *apigen.InternalUser) {
		u.Name = " operator "
	})
	loginRes, err := h.PostV1AuthPasswordLogin(apigen.Context{}, &apigen.PasswordLoginRequest{Username: " operator ", Password: "correct horse battery"})
	if err != nil {
		t.Fatalf("PostV1AuthPasswordLogin: %v", err)
	}
	if loginRes.UserID != user.ID || len(loginRes.Scopes) != 1 || loginRes.Scopes[0] != ScopeDefault {
		t.Fatalf("login response = %+v, want user %d with [default]", loginRes, user.ID)
	}
	if _, _, err := h.jwtAuth.VerifyAndResolveUser(loginRes.Token); err != nil {
		t.Fatalf("issued token does not verify: %v", err)
	}

	for _, tc := range []struct{ name, user, password string }{
		{"wrong password", "operator", "not the password"},
		{"unknown user", "nobody", "correct horse battery"},
		{"empty password", "operator", ""},
	} {
		_, err := h.PostV1AuthPasswordLogin(apigen.Context{}, &apigen.PasswordLoginRequest{Username: tc.user, Password: tc.password})
		if !errors.Is(err, InvalidPasswordLoginErr) {
			t.Fatalf("%s: err = %v, want InvalidPasswordLoginErr", tc.name, err)
		}
	}
}

func TestPasswordSetRejectsShortAndDelegated(t *testing.T) {
	h, user := newAuthTestHandler(t)
	enablePasswordLogin(t, h)

	_, err := h.PostV1AuthPasswordSet(apigen.Context{User: user}, &apigen.PasswordSetRequest{Password: "short"})
	if !errors.Is(err, PasswordTooShortErr) {
		t.Fatalf("short err = %v, want PasswordTooShortErr", err)
	}
	delegated := *user
	delegated.Delegated = true
	_, err = h.PostV1AuthPasswordSet(apigen.Context{User: &delegated}, &apigen.PasswordSetRequest{Password: "correct horse battery"})
	if !errors.Is(err, DelegationNotPermittedErr) {
		t.Fatalf("delegated err = %v, want DelegationNotPermittedErr", err)
	}
	stored, err := h.Store.FetchUserByID(user.ID)
	if err != nil {
		t.Fatalf("FetchUserByID: %v", err)
	}
	if stored.PasswordHash != "" {
		t.Fatalf("rejected sets still stored a hash %q", stored.PasswordHash)
	}
}

func TestPasswordSetWithDefaultSessionEchoesThatSession(t *testing.T) {
	h, user := newAuthTestHandler(t)
	enablePasswordLogin(t, h)
	token := h.mustToken(t, user.ID, []string{ScopeDefault}, time.Hour)

	res, err := h.PostV1AuthPasswordSet(apigen.Context{User: user, Token: token}, &apigen.PasswordSetRequest{Password: "correct horse battery"})
	if err != nil {
		t.Fatalf("PostV1AuthPasswordSet: %v", err)
	}
	if res.Token != token {
		t.Fatalf("set with a default session minted a new token; want the caller's own echoed back")
	}
	if sessions, _ := h.Store.ListPersonalSessionsForUser(user.ID); len(sessions) != 0 {
		t.Fatalf("set with a default session created %d personal session rows, want 0", len(sessions))
	}

	bootstrap := h.mustToken(t, user.ID, []string{ScopePasskeyCreate}, time.Hour)
	res, err = h.PostV1AuthPasswordSet(apigen.Context{User: user, Token: bootstrap}, &apigen.PasswordSetRequest{Password: "correct horse battery"})
	if err != nil {
		t.Fatalf("PostV1AuthPasswordSet (bootstrap): %v", err)
	}
	if res.Token == bootstrap || len(res.Scopes) != 1 || res.Scopes[0] != ScopeDefault {
		t.Fatalf("set with a bootstrap token did not mint a default session: %+v", res)
	}
}
