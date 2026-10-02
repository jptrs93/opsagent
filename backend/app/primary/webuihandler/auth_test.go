package webuihandler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// newAuthTestHandler builds a Handler with just enough wiring to mint and
// verify session tokens against a throwaway store.
func newAuthTestHandler(t *testing.T) (*Handler, *apigen.InternalUser) {
	t.Helper()
	dir := t.TempDir()
	store := state.Open(filepath.Join(dir, "primary.db"))
	secretManager, err := secrets.Initialize(dir, store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	// InitializeService rather than NewService: nothing has written a primary
	// config into this throwaway store, and NewService pointedly refuses to
	// invent one.
	configService, err := systemconfig.InitializeService(store, apigen.SystemConfig{})
	if err != nil {
		t.Fatalf("systemconfig.InitializeService: %v", err)
	}
	h := &Handler{Store: store, Queries: store.Queries(), Secrets: secretManager, SystemConfig: configService}
	webAuthNID, err := authu.GenerateWebAuthnID(32)
	if err != nil {
		t.Fatalf("GenerateWebAuthnID: %v", err)
	}
	user := &apigen.InternalUser{ID: 1, WebAuthNID: webAuthNID, Name: "operator"}
	users.Write(store, user)
	return h, user
}

// fullSessionPolicy is what every ordinary route declares: a full session,
// never a bootstrap one.
var fullSessionPolicy = apigen.AccessPolicy{PolicyType: apigen.AccessPolicyType_ANY_OF, Scopes: []string{"full"}}

const (
	fullSession      = apigen.UserSessionKind_USER_SESSION_KIND_FULL
	bootstrapSession = apigen.UserSessionKind_USER_SESSION_KIND_BOOTSTRAP
)

// mustToken opens a user session of the given kind and lifetime and returns
// its bearer token. A negative ttl yields an already-expired session.
func (h *Handler) mustToken(t *testing.T, userID int32, kind apigen.UserSessionKind, ttl time.Duration) string {
	t.Helper()
	user, err := users.ByID(h.Store.Queries(), userID)
	if err != nil {
		t.Fatalf("users.ByID(%d): %v", userID, err)
	}
	res, err := h.startUserSession(bg(), user, kind, ttl)
	if err != nil {
		t.Fatalf("startUserSession: %v", err)
	}
	return res.Token
}

// verifyToken runs a token through VerifyAuth under a policy that accepts any
// session kind, so callers can inspect what the token resolved to.
func (h *Handler) verifyToken(token string) (apigen.Context, error) {
	r := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	return h.VerifyAuth(context.Background(), httptest.NewRecorder(), r, apigen.AccessPolicy{PolicyType: apigen.AccessPolicyType_OPTIONAL_AUTH})
}

func TestPostV1AgentSessionsCreateMintsShortLivedToken(t *testing.T) {
	h, user := newAuthTestHandler(t)
	session := h.operatorCtx(t, user)

	before := time.Now()
	res, err := h.PostV1AgentSessionsCreate(session)
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}
	if res.Token == "" {
		t.Fatal("expected a token")
	}
	if res.Token == session.Token {
		t.Fatal("expected a newly minted token, not the caller's session token echoed back")
	}
	if !strings.HasPrefix(res.Token, agentTokenKind) {
		t.Fatalf("token %q does not carry the agent prefix", res.Token)
	}

	wantExpiry := before.Add(agentSessionTTL)
	if res.Session.ExpiresAt.Before(wantExpiry.Add(-time.Minute)) || res.Session.ExpiresAt.After(wantExpiry.Add(time.Minute)) {
		t.Errorf("expiry = %v, want ~%v", res.Session.ExpiresAt, wantExpiry)
	}
	// The reported expiry must match what the server enforces, or the UI
	// would show a lifetime the server does not honour.
	verified, err := h.verifyToken(res.Token)
	if err != nil {
		t.Fatalf("minted token does not verify: %v", err)
	}
	if diff := verified.SessionExpiresAt.Sub(res.Session.ExpiresAt); diff > time.Minute || diff < -time.Minute {
		t.Errorf("enforced expiry %v disagrees with reported expiry %v", verified.SessionExpiresAt, res.Session.ExpiresAt)
	}
}

// Every way a token can be wrong must fail closed at VerifyAuth.
func TestVerifyAuthRejectsBadTokens(t *testing.T) {
	h, user := newAuthTestHandler(t)
	good := h.mustToken(t, user.ID, fullSession, time.Hour)
	kind, id, ok := splitToken(good)
	if !ok || kind != userTokenKind {
		t.Fatalf("splitToken(%q) = %q %q %v", good, kind, id, ok)
	}
	for name, token := range map[string]string{
		"empty":            "",
		"garbage":          "not-a-token",
		"unknown id":       userTokenKind + "nope.secret",
		"wrong secret":     userTokenKind + id + ".wrong",
		"kind swapped":     agentTokenKind + strings.TrimPrefix(good, userTokenKind),
		"no separator":     userTokenKind + id,
		"expired":          h.mustToken(t, user.ID, fullSession, -time.Minute),
		"pending agent id": agentTokenKind + h.mustRequestStart(t, user.ID).ID + ".secret",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := h.verifyToken(token); err == nil {
				t.Fatal("expected VerifyAuth to reject the token")
			}
		})
	}
	if _, err := h.verifyToken(good); err != nil {
		t.Fatalf("the untouched token must still verify: %v", err)
	}
}

// A user session revoked from the sessions page must stop authenticating on
// its next request, and must not affect the user's other sessions.
func TestRevokedUserSessionTokenFailsVerifyAuth(t *testing.T) {
	h, user := newAuthTestHandler(t)
	first := h.mustToken(t, user.ID, fullSession, time.Hour)
	second := h.mustToken(t, user.ID, fullSession, time.Hour)
	ctx, err := h.verifyToken(first)
	if err != nil {
		t.Fatalf("VerifyAuth: %v", err)
	}
	if err := h.PostV1UserSessionsRevoke(ctx, &apigen.UserSessionRevokeRequest{ID: ctx.SessionID}); err != nil {
		t.Fatalf("PostV1UserSessionsRevoke: %v", err)
	}
	if _, err := h.verifyToken(first); err == nil {
		t.Fatal("revoked session still authenticates")
	}
	if _, err := h.verifyToken(second); err != nil {
		t.Fatalf("unrelated session was affected: %v", err)
	}
}

// Exercises the real generated route, so routing and policy enforcement are
// covered rather than just the handler method.
func TestAgentSessionsCreateRouteRejectsBootstrapSession(t *testing.T) {
	h, user := newAuthTestHandler(t)
	mux := apigen.CreateApiServerMux(h, &apigen.MuxConfig{VerifyAuth: h.VerifyAuth})

	call := func(authHeader string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/agent-sessions/create", nil)
		if authHeader != "" {
			r.Header.Set("Authorization", authHeader)
		}
		w := httptest.NewRecorder()
		mux.ServeHTTP(w, r)
		return w
	}

	t.Run("full session succeeds", func(t *testing.T) {
		w := call("Bearer " + h.mustToken(t, user.ID, fullSession, time.Hour))
		if w.Code != http.StatusOK {
			t.Fatalf("status = %d, want 200 (body %s)", w.Code, w.Body.String())
		}
		res, err := apigen.DecodeAgentSessionCreated(w.Body.Bytes())
		if err != nil {
			t.Fatalf("decoding response: %v", err)
		}
		if res.Token == "" {
			t.Fatal("expected a token in the response")
		}
	})

	t.Run("no auth is rejected", func(t *testing.T) {
		if w := call(""); w.Code != http.StatusUnauthorized {
			t.Fatalf("status = %d, want 401", w.Code)
		}
	})

	// A bootstrap token exists only to register a passkey. It must not be able
	// to mint a general-access agent token.
	t.Run("bootstrap session is rejected", func(t *testing.T) {
		w := call("Bearer " + h.mustToken(t, user.ID, bootstrapSession, time.Hour))
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", w.Code, w.Body.String())
		}
	})
}

// End to end: a token from this endpoint must actually authenticate against an
// ordinary route.
func TestGeneratedTokenAuthenticatesRequests(t *testing.T) {
	h, user := newAuthTestHandler(t)
	session := h.operatorCtx(t, user)
	res, err := h.PostV1AgentSessionsCreate(session)
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	r.Header.Set("Authorization", "Bearer "+res.Token)
	policy := fullSessionPolicy

	authCtx, err := h.VerifyAuth(context.Background(), httptest.NewRecorder(), r, policy)
	if err != nil {
		t.Fatalf("generated token failed VerifyAuth: %v", err)
	}
	if authCtx.User == nil || authCtx.User.ID != user.ID {
		t.Fatalf("resolved user = %#v, want id %d", authCtx.User, user.ID)
	}
	// Agent tokens resolve a delegated user; the plain browser session must
	// not.
	if !authCtx.User.Delegated {
		t.Fatal("agent-session token should resolve a delegated user")
	}
	if authCtx.SessionID != res.Session.ID {
		t.Fatalf("resolved session id %q, want %q", authCtx.SessionID, res.Session.ID)
	}
	r = httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	r.Header.Set("Authorization", "Bearer "+session.Token)
	sessionCtx, err := h.VerifyAuth(context.Background(), httptest.NewRecorder(), r, policy)
	if err != nil {
		t.Fatalf("session token failed VerifyAuth: %v", err)
	}
	if sessionCtx.User == nil || sessionCtx.User.Delegated {
		t.Fatalf("browser session token must not resolve a delegated user: %#v", sessionCtx.User)
	}
}

// The plaintext token must not be recoverable from storage. Only its hash is
// persisted, so a copy of the database carries no usable credential.
func TestAgentSessionStoresOnlyTokenHash(t *testing.T) {
	h, user := newAuthTestHandler(t)
	res, err := h.PostV1AgentSessionsCreate(h.operatorCtx(t, user))
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}
	rec, err := h.agentSessions().FetchAgentSession(res.Session.ID)
	if err != nil {
		t.Fatalf("FetchAgentSession: %v", err)
	}
	if bytes.Contains(rec.TokenHash, []byte(res.Token)) {
		t.Fatal("stored hash contains the plaintext token")
	}
	want := sha256.Sum256([]byte(res.Token))
	if !bytes.Equal(rec.TokenHash, want[:]) {
		t.Fatal("stored hash is not the SHA-256 of the issued token")
	}
	if !strings.HasPrefix(res.Token, rec.TokenPrefix) {
		t.Fatalf("token prefix %q is not a prefix of the token", rec.TokenPrefix)
	}
	if len(rec.TokenPrefix) >= len(res.Token) {
		t.Fatal("stored prefix is the whole token")
	}
}

func TestAgentSessionsListReturnsOnlyTheCallersSessions(t *testing.T) {
	h, user := newAuthTestHandler(t)
	other := &apigen.InternalUser{ID: 2, WebAuthNID: user.WebAuthNID, Name: "other"}
	users.Write(h.Store, other)

	mine, err := h.PostV1AgentSessionsCreate(h.operatorCtx(t, user))
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}
	if _, err := h.PostV1AgentSessionsCreate(h.operatorCtx(t, other)); err != nil {
		t.Fatalf("PostV1AgentSessionsCreate for other user: %v", err)
	}

	list, err := h.PostV1AgentSessionsList(apigen.Context{Ctx: context.Background(), User: user})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsList: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].ID != mine.Session.ID {
		t.Fatalf("list = %#v, want only the caller's own session", list.Items)
	}
	// The list is metadata only; the token never reappears.
	if list.Items[0].TokenPrefix == mine.Token {
		t.Fatal("list exposed the full token")
	}
}

// Revocation must actually stop the token, not just change how it is displayed.
func TestRevokedAgentSessionTokenFailsVerifyAuth(t *testing.T) {
	h, user := newAuthTestHandler(t)
	res, err := h.PostV1AgentSessionsCreate(h.operatorCtx(t, user))
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}

	policy := fullSessionPolicy
	verify := func() error {
		r := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
		r.Header.Set("Authorization", "Bearer "+res.Token)
		_, err := h.VerifyAuth(context.Background(), httptest.NewRecorder(), r, policy)
		return err
	}

	if err := verify(); err != nil {
		t.Fatalf("token failed VerifyAuth before revocation: %v", err)
	}
	if err := h.PostV1AgentSessionsRevoke(
		apigen.Context{Ctx: context.Background(), User: user},
		&apigen.AgentSessionRevokeRequest{ID: res.Session.ID},
	); err != nil {
		t.Fatalf("PostV1AgentSessionsRevoke: %v", err)
	}
	if err := verify(); err == nil {
		t.Fatal("revoked token still authenticates")
	}

	list, err := h.PostV1AgentSessionsList(apigen.Context{Ctx: context.Background(), User: user})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsList: %v", err)
	}
	if len(list.Items) != 1 || list.Items[0].Status != apigen.AgentSessionStatus_AGENT_SESSION_REVOKED {
		t.Fatalf("list = %#v, want the session marked revoked", list.Items)
	}
}

// One operator must not be able to revoke another's session by guessing its id.
func TestAgentSessionRevokeIsScopedToTheOwner(t *testing.T) {
	h, user := newAuthTestHandler(t)
	other := &apigen.InternalUser{ID: 2, WebAuthNID: user.WebAuthNID, Name: "other"}
	users.Write(h.Store, other)

	res, err := h.PostV1AgentSessionsCreate(h.operatorCtx(t, user))
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}

	err = h.PostV1AgentSessionsRevoke(
		apigen.Context{Ctx: context.Background(), User: other},
		&apigen.AgentSessionRevokeRequest{ID: res.Session.ID},
	)
	if err == nil {
		t.Fatal("expected another user's revoke to fail")
	}
	rec, fetchErr := h.agentSessions().FetchAgentSession(res.Session.ID)
	if fetchErr != nil {
		t.Fatalf("FetchAgentSession: %v", fetchErr)
	}
	if rec.Status == apigen.AgentSessionStatus_AGENT_SESSION_REVOKED || rec.Status == apigen.AgentSessionStatus_AGENT_SESSION_REJECTED {
		t.Fatal("session was revoked by a different user")
	}
}
