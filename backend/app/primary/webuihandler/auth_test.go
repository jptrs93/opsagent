package webuihandler

import (
	"bytes"
	"context"
	"crypto/sha256"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/config"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
	"github.com/jptrs93/opsagent/backend/storage/primarydb"
	"github.com/jptrs93/opsagent/backend/util/jwtu"
)

// newAuthTestHandler builds a Handler with just enough wiring to mint and
// verify JWTs against a throwaway store.
func newAuthTestHandler(t *testing.T) (*Handler, *apigen.InternalUser) {
	t.Helper()
	dir := t.TempDir()
	store := primarydb.Open(filepath.Join(dir, "primary.db"))
	secretManager, err := secrets.Initialize(dir, store)
	if err != nil {
		t.Fatalf("secrets.Initialize: %v", err)
	}
	// InitializeService rather than NewService: nothing has written a primary
	// config into this throwaway store, and NewService pointedly refuses to
	// invent one.
	configService, err := config.InitializeService(store, apigen.PrimaryConfig{})
	if err != nil {
		t.Fatalf("config.InitializeService: %v", err)
	}
	h := &Handler{Store: store, Secrets: secretManager, ConfigService: configService}
	h.jwtAuth = authu.NewJWTAuth[*apigen.InternalUser, int32](
		func(kid string, key []byte) error {
			h.Store.WritePublicKey(&apigen.PublicKeyRecord{Kid: kid, KeyBytes: key})
			return nil
		},
		func(kid string) ([]byte, error) {
			rec, err := h.Store.FetchPublicKey(kid)
			if err != nil {
				return nil, err
			}
			return rec.KeyBytes, nil
		},
		func(id int32) (*apigen.InternalUser, error) {
			return h.Store.FetchUserByID(id)
		},
	)
	webAuthNID, err := authu.GenerateWebAuthnID(32)
	if err != nil {
		t.Fatalf("GenerateWebAuthnID: %v", err)
	}
	user := &apigen.InternalUser{ID: 1, WebAuthNID: webAuthNID, Name: "operator"}
	store.WriteUser(user)
	return h, user
}

func (h *Handler) mustToken(t *testing.T, userID int32, scopes []string, ttl time.Duration) string {
	t.Helper()
	token, err := h.jwtAuth.GenerateTokenWith(userID, scopes, ttl)
	if err != nil {
		t.Fatalf("GenerateTokenWith: %v", err)
	}
	return token
}

func TestPostV1AgentSessionsCreateMintsShortLivedToken(t *testing.T) {
	h, user := newAuthTestHandler(t)
	session := h.mustToken(t, user.ID, []string{"default"}, 48*time.Hour)

	before := time.Now()
	res, err := h.PostV1AgentSessionsCreate(apigen.Context{Ctx: context.Background(), User: user, Token: session})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}
	if res.Token == "" {
		t.Fatal("expected a token")
	}
	if res.Token == session {
		t.Fatal("expected a newly minted token, not the caller's session token echoed back")
	}

	wantExpiry := before.Add(agentSessionTTL)
	if res.Session.ExpiresAt.Before(wantExpiry.Add(-time.Minute)) || res.Session.ExpiresAt.After(wantExpiry.Add(time.Minute)) {
		t.Errorf("expiry = %v, want ~%v", res.Session.ExpiresAt, wantExpiry)
	}
	if !reflect.DeepEqual(res.Session.Scopes, []string{"default"}) {
		t.Errorf("scopes = %#v, want [default]", res.Session.Scopes)
	}

	// The reported expiry must match what is actually signed into the token,
	// or the UI would show a lifetime the server does not honour.
	claims, _, err := h.jwtAuth.VerifyAndResolveUser(res.Token)
	if err != nil {
		t.Fatalf("minted token does not verify: %v", err)
	}
	tokenExpiry, err := jwtu.ExpiryFromClaims(claims)
	if err != nil {
		t.Fatalf("ExpiryFromClaims: %v", err)
	}
	if diff := tokenExpiry.Sub(res.Session.ExpiresAt); diff > time.Minute || diff < -time.Minute {
		t.Errorf("signed expiry %v disagrees with reported expiry %v", tokenExpiry, res.Session.ExpiresAt)
	}
}

// The token must carry the caller's own scopes rather than a fixed list, so it
// can never grant more than the session that asked for it.
func TestPostV1AgentSessionsCreateDoesNotEscalateScopes(t *testing.T) {
	h, user := newAuthTestHandler(t)
	session := h.mustToken(t, user.ID, []string{"default", "custom:scope"}, time.Hour)

	res, err := h.PostV1AgentSessionsCreate(apigen.Context{Ctx: context.Background(), User: user, Token: session})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}
	if !reflect.DeepEqual(res.Session.Scopes, []string{"default", "custom:scope"}) {
		t.Fatalf("scopes = %#v, want the caller's own scopes", res.Session.Scopes)
	}
	claims, _, err := h.jwtAuth.VerifyAndResolveUser(res.Token)
	if err != nil {
		t.Fatalf("minted token does not verify: %v", err)
	}
	if got := jwtu.ScopesFromClaims(claims); !reflect.DeepEqual(got, []string{"default", "custom:scope"}) {
		t.Fatalf("signed scopes = %#v, want the caller's own scopes", got)
	}
}

func TestPostV1AgentSessionsCreateRejectsBadToken(t *testing.T) {
	h, user := newAuthTestHandler(t)
	for name, token := range map[string]string{
		"empty":   "",
		"garbage": "not-a-jwt",
		"expired": h.mustToken(t, user.ID, []string{"default"}, -time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.PostV1AgentSessionsCreate(apigen.Context{Ctx: context.Background(), User: user, Token: token})
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// Exercises the real generated route, so routing and policy enforcement are
// covered rather than just the handler method.
func TestAgentSessionsCreateRouteEnforcesScopes(t *testing.T) {
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

	t.Run("default scope succeeds", func(t *testing.T) {
		w := call("Bearer " + h.mustToken(t, user.ID, []string{"default"}, time.Hour))
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
	t.Run("passkey:create scope is rejected", func(t *testing.T) {
		w := call("Bearer " + h.mustToken(t, user.ID, []string{"passkey:create"}, time.Hour))
		if w.Code != http.StatusForbidden {
			t.Fatalf("status = %d, want 403 (body %s)", w.Code, w.Body.String())
		}
	})
}

// End to end: a token from this endpoint must actually authenticate against a
// default-scope route.
func TestGeneratedTokenAuthenticatesRequests(t *testing.T) {
	h, user := newAuthTestHandler(t)
	session := h.mustToken(t, user.ID, []string{"default"}, 48*time.Hour)
	res, err := h.PostV1AgentSessionsCreate(apigen.Context{Ctx: context.Background(), User: user, Token: session})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}

	r := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	r.Header.Set("Authorization", "Bearer "+res.Token)
	policy := apigen.AccessPolicy{PolicyType: apigen.AccessPolicyType_ANY_OF, Scopes: []string{"default"}}

	authCtx, err := h.VerifyAuth(context.Background(), httptest.NewRecorder(), r, policy)
	if err != nil {
		t.Fatalf("generated token failed VerifyAuth: %v", err)
	}
	if authCtx.User == nil || authCtx.User.ID != user.ID {
		t.Fatalf("resolved user = %#v, want id %d", authCtx.User, user.ID)
	}
}

// The plaintext token must not be recoverable from storage. Only its hash is
// persisted, so a copy of the database carries no usable credential.
func TestAgentSessionStoresOnlyTokenHash(t *testing.T) {
	h, user := newAuthTestHandler(t)
	session := h.mustToken(t, user.ID, []string{"default"}, 48*time.Hour)

	res, err := h.PostV1AgentSessionsCreate(apigen.Context{Ctx: context.Background(), User: user, Token: session})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}
	rec, err := h.Store.FetchAgentSession(res.Session.ID)
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
	h.Store.WriteUser(other)

	mine, err := h.PostV1AgentSessionsCreate(apigen.Context{
		Ctx: context.Background(), User: user, Token: h.mustToken(t, user.ID, []string{"default"}, time.Hour),
	})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}
	if _, err := h.PostV1AgentSessionsCreate(apigen.Context{
		Ctx: context.Background(), User: other, Token: h.mustToken(t, other.ID, []string{"default"}, time.Hour),
	}); err != nil {
		t.Fatalf("PostV1AgentSessionsCreate for other user: %v", err)
	}

	list, err := h.PostV1AgentSessionsList(apigen.Context{Ctx: context.Background(), User: user}, &apigen.EmptyRequest{})
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
	session := h.mustToken(t, user.ID, []string{"default"}, 48*time.Hour)
	res, err := h.PostV1AgentSessionsCreate(apigen.Context{Ctx: context.Background(), User: user, Token: session})
	if err != nil {
		t.Fatalf("PostV1AgentSessionsCreate: %v", err)
	}

	policy := apigen.AccessPolicy{PolicyType: apigen.AccessPolicyType_ANY_OF, Scopes: []string{"default"}}
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

	list, err := h.PostV1AgentSessionsList(apigen.Context{Ctx: context.Background(), User: user}, &apigen.EmptyRequest{})
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
	h.Store.WriteUser(other)

	res, err := h.PostV1AgentSessionsCreate(apigen.Context{
		Ctx: context.Background(), User: user, Token: h.mustToken(t, user.ID, []string{"default"}, time.Hour),
	})
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
	rec, fetchErr := h.Store.FetchAgentSession(res.Session.ID)
	if fetchErr != nil {
		t.Fatalf("FetchAgentSession: %v", fetchErr)
	}
	if !rec.RevokedAt.IsZero() {
		t.Fatal("session was revoked by a different user")
	}
}

// Browser session and bootstrap tokens carry no jti, so they must keep working
// without any agent_sessions row backing them.
func TestTokensWithoutSessionIDStillVerify(t *testing.T) {
	h, user := newAuthTestHandler(t)
	r := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	r.Header.Set("Authorization", "Bearer "+h.mustToken(t, user.ID, []string{"default"}, time.Hour))
	policy := apigen.AccessPolicy{PolicyType: apigen.AccessPolicyType_ANY_OF, Scopes: []string{"default"}}

	if _, err := h.VerifyAuth(context.Background(), httptest.NewRecorder(), r, policy); err != nil {
		t.Fatalf("session token without jti failed VerifyAuth: %v", err)
	}
}
