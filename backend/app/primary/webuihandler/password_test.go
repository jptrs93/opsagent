package webuihandler

import (
	"errors"
	"testing"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
)

const (
	testMasterPassword = "opendeploy-test-master-password"
	testUserPassword   = "correct horse battery"
)

// setupAuth stores the master password, wires authz, and gives the test
// user a password.
func setupAuth(t *testing.T, h *Handler, user *apigen.User) {
	t.Helper()
	hash, err := authu.HashPassword(testMasterPassword)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if err := h.SystemConfig.SetMasterPasswordHash(hash, 0); err != nil {
		t.Fatalf("SetMasterPasswordHash: %v", err)
	}
	authzService, err := authz.Open(h.Store)
	if err != nil {
		t.Fatalf("authz.Open: %v", err)
	}
	h.Authz = authzService
	if user != nil {
		setPassword(t, h, user.ID, testUserPassword)
	}
}

func setPassword(t *testing.T, h *Handler, userID uint64, password string) {
	t.Helper()
	hash, err := authu.HashPassword(password)
	if err != nil {
		t.Fatalf("HashPassword: %v", err)
	}
	if _, err := users.Update(h.Store, userID, 0, func(u *apigen.User) error {
		u.PasswordHash = apigen.Some(hash)
		return nil
	}); err != nil {
		t.Fatalf("set password: %v", err)
	}
}

func setRequirePasskey(t *testing.T, h *Handler, on bool) {
	t.Helper()
	settings := h.SystemConfig.Snapshot().Settings
	settings.Auth.RequirePasskey = systemconfig.BoolLiteral(on)
	if err := h.SystemConfig.UpdateSettings(settings, 0, nil); err != nil {
		t.Fatalf("UpdateSettings: %v", err)
	}
}

func TestPasswordLoginUsesTheUsersOwnPassword(t *testing.T) {
	h, user := newAuthTestHandler(t)
	setupAuth(t, h, user)
	methods, err := h.GetV1AuthMethods(apigen.Context{})
	if err != nil || methods.RequirePasskey || methods.LocalCaAvailable {
		t.Fatalf("methods = %+v, err %v; want password sign-in on and no local CA", methods, err)
	}

	// Padded stored name, padded input: both sides trim.
	users.UpdateMatching(h.Store, func(u *apigen.User) bool { return u.ID == user.ID }, func(u *apigen.User) {
		u.Name = apigen.Some(" operator ")
	})
	res, err := h.PostV1AuthPasswordLogin(bg(), &apigen.PasswordLoginRequest{Username: " operator ", Password: testUserPassword})
	if err != nil {
		t.Fatalf("PostV1AuthPasswordLogin: %v", err)
	}
	if res.UserID != user.ID || res.Kind != fullSession || res.Name != " operator " {
		t.Fatalf("login response = %+v, want a full session for user %d", res, user.ID)
	}
	if _, err := h.verifyToken(res.Token); err != nil {
		t.Fatalf("issued token does not verify: %v", err)
	}
	if sessions, _ := users.ListUserSessions(h.Store.Queries(), user.ID); len(sessions) != 1 {
		t.Fatalf("user sessions = %d, want 1", len(sessions))
	}
	// The first full session activates the user.
	if activated, err := users.ByID(h.Store.Queries(), user.ID); err != nil || !activated.ActivatedAt.Present {
		t.Fatalf("user after login = %+v, %v; want activated", activated, err)
	}

	// Nothing is created on the fly any more, and a user without a password
	// cannot use the route.
	users.Write(h.Store, &apigen.User{ID: 2, Name: apigen.Some("passkey-only")})
	for _, tc := range []struct {
		name, user, password string
		want                 error
	}{
		{"wrong password", "operator", "not the password", InvalidPasswordLoginErr},
		{"master password is not a user password", "operator", testMasterPassword, InvalidPasswordLoginErr},
		{"empty password", "operator", "", InvalidPasswordLoginErr},
		{"unknown user", "newcomer", testUserPassword, InvalidPasswordLoginErr},
		{"user without a password", "passkey-only", testUserPassword, InvalidPasswordLoginErr},
		{"empty username", "", testUserPassword, UsernameRequiredErr},
	} {
		_, err := h.PostV1AuthPasswordLogin(bg(), &apigen.PasswordLoginRequest{Username: tc.user, Password: tc.password})
		if !errors.Is(err, tc.want) {
			t.Fatalf("%s: err = %v, want %v", tc.name, err, tc.want)
		}
	}
	if _, err := users.ByName(h.Store.Queries(), "newcomer"); !errors.Is(err, users.ErrNotFound) {
		t.Fatalf("a failed login must not create a user: %v", err)
	}
}

func TestRequirePasskeyTurnsPasswordLoginOff(t *testing.T) {
	h, user := newAuthTestHandler(t)
	setupAuth(t, h, user)
	setRequirePasskey(t, h, true)
	methods, err := h.GetV1AuthMethods(apigen.Context{})
	if err != nil || !methods.RequirePasskey {
		t.Fatalf("methods = %+v, err %v; want require_passkey", methods, err)
	}
	if _, err := h.PostV1AuthPasswordLogin(bg(), &apigen.PasswordLoginRequest{Username: "operator", Password: testUserPassword}); !errors.Is(err, PasskeyRequiredErr) {
		t.Fatalf("login err = %v, want PasskeyRequiredErr", err)
	}
	setRequirePasskey(t, h, false)
	if _, err := h.PostV1AuthPasswordLogin(bg(), &apigen.PasswordLoginRequest{Username: "operator", Password: testUserPassword}); err != nil {
		t.Fatalf("login after turning the setting off: %v", err)
	}
}

// The master password opens a root session: a real row of the ROOT kind
// with no user and a short life, accepted only by the root routes.
func TestMasterPasswordOpensRootSession(t *testing.T) {
	h, _ := newAuthTestHandler(t)
	setupAuth(t, h, nil)

	if _, err := h.PostV1AuthMaster(bg(), &apigen.MasterPasswordRequest{Password: "wrong"}); !errors.Is(err, InvalidMasterPasswordErr) {
		t.Fatalf("wrong master password err = %v", err)
	}
	res, err := h.PostV1AuthMaster(bg(), &apigen.MasterPasswordRequest{Password: testMasterPassword})
	if err != nil {
		t.Fatalf("PostV1AuthMaster: %v", err)
	}
	if res.Kind != rootSession || res.UserID != 0 || res.Name != "" {
		t.Fatalf("response = %+v, want a ROOT session with no user", res)
	}
	if until := time.Until(res.Expiry); until > rootSessionTTL || until < rootSessionTTL-time.Minute {
		t.Fatalf("expiry in %v, want about %v", until, rootSessionTTL)
	}
	ctx, err := h.verifyToken(res.Token)
	if err != nil {
		t.Fatalf("root token does not verify: %v", err)
	}
	if ctx.User != nil || ctx.SessionKind != rootSession {
		t.Fatalf("resolved context = user %v kind %v", ctx.User, ctx.SessionKind)
	}
	if err := fullSessionPolicy.CanAccess(ctx.SessionKind); err == nil {
		t.Fatal("root session passed a full-session policy")
	}
	rootPolicy := apigen.AccessPolicy{PolicyType: apigen.AccessPolicyType_ANY_OF, Scopes: []string{"full", "root"}}
	if err := rootPolicy.CanAccess(ctx.SessionKind); err != nil {
		t.Fatalf("root session failed a root policy: %v", err)
	}
	current, err := h.GetV1AuthCurrentSession(ctx)
	if err != nil || current.Kind != rootSession || current.SessionID != res.SessionID {
		t.Fatalf("current session = %+v, %v", current, err)
	}
	// Root rows belong to nobody, so no user's session list carries them.
	if sessions, err := h.Queries.ListAllUserSessions(ctx); err != nil || len(sessions) != 1 || sessions[0].UserID != 0 || sessions[0].Kind != int64(rootSession) {
		t.Fatalf("stored sessions = %+v, %v", sessions, err)
	}
	if err := requireHuman(ctx); !errors.Is(err, InvalidAuthTokenErr) {
		t.Fatalf("requireHuman on root = %v, want InvalidAuthTokenErr", err)
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
