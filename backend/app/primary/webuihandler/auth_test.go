package webuihandler

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
	"github.com/jptrs93/opsagent/backend/util/jwtu"
)

// newAuthTestHandler builds a Handler with just enough wiring to mint and
// verify JWTs against a throwaway store.
func newAuthTestHandler(t *testing.T) (*Handler, *apigen.InternalUser) {
	t.Helper()
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	h := &Handler{Store: store}
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

func TestPostV1AuthTokenGenerateMints12HourToken(t *testing.T) {
	h, user := newAuthTestHandler(t)
	session := h.mustToken(t, user.ID, []string{"default"}, 48*time.Hour)

	before := time.Now()
	res, err := h.PostV1AuthTokenGenerate(apigen.Context{Ctx: context.Background(), User: user, Token: session})
	if err != nil {
		t.Fatalf("PostV1AuthTokenGenerate: %v", err)
	}
	if res.Token == "" {
		t.Fatal("expected a token")
	}
	if res.Token == session {
		t.Fatal("expected a newly minted token, not the caller's session token echoed back")
	}

	wantExpiry := before.Add(12 * time.Hour)
	if res.Expiry.Before(wantExpiry.Add(-time.Minute)) || res.Expiry.After(wantExpiry.Add(time.Minute)) {
		t.Errorf("expiry = %v, want ~%v", res.Expiry, wantExpiry)
	}
	if !reflect.DeepEqual(res.Scopes, []string{"default"}) {
		t.Errorf("scopes = %#v, want [default]", res.Scopes)
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
	if diff := tokenExpiry.Sub(res.Expiry); diff > time.Minute || diff < -time.Minute {
		t.Errorf("signed expiry %v disagrees with reported expiry %v", tokenExpiry, res.Expiry)
	}
}

// The token must carry the caller's own scopes rather than a fixed list, so it
// can never grant more than the session that asked for it.
func TestPostV1AuthTokenGenerateDoesNotEscalateScopes(t *testing.T) {
	h, user := newAuthTestHandler(t)
	session := h.mustToken(t, user.ID, []string{"default", "custom:scope"}, time.Hour)

	res, err := h.PostV1AuthTokenGenerate(apigen.Context{Ctx: context.Background(), User: user, Token: session})
	if err != nil {
		t.Fatalf("PostV1AuthTokenGenerate: %v", err)
	}
	if !reflect.DeepEqual(res.Scopes, []string{"default", "custom:scope"}) {
		t.Fatalf("scopes = %#v, want the caller's own scopes", res.Scopes)
	}
	claims, _, err := h.jwtAuth.VerifyAndResolveUser(res.Token)
	if err != nil {
		t.Fatalf("minted token does not verify: %v", err)
	}
	if got := jwtu.ScopesFromClaims(claims); !reflect.DeepEqual(got, []string{"default", "custom:scope"}) {
		t.Fatalf("signed scopes = %#v, want the caller's own scopes", got)
	}
}

func TestPostV1AuthTokenGenerateRejectsBadToken(t *testing.T) {
	h, user := newAuthTestHandler(t)
	for name, token := range map[string]string{
		"empty":   "",
		"garbage": "not-a-jwt",
		"expired": h.mustToken(t, user.ID, []string{"default"}, -time.Minute),
	} {
		t.Run(name, func(t *testing.T) {
			_, err := h.PostV1AuthTokenGenerate(apigen.Context{Ctx: context.Background(), User: user, Token: token})
			if err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// Exercises the real generated route, so routing and policy enforcement are
// covered rather than just the handler method.
func TestAuthTokenGenerateRouteEnforcesScopes(t *testing.T) {
	h, user := newAuthTestHandler(t)
	mux := apigen.CreateOpsagentHttpV1Mux(h, &apigen.MuxConfig{VerifyAuth: h.VerifyAuth})

	call := func(authHeader string) *httptest.ResponseRecorder {
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/token/generate", nil)
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
		res, err := apigen.DecodeApiTokenResponse(w.Body.Bytes())
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
	// to mint a 12-hour general-access token.
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
	res, err := h.PostV1AuthTokenGenerate(apigen.Context{Ctx: context.Background(), User: user, Token: session})
	if err != nil {
		t.Fatalf("PostV1AuthTokenGenerate: %v", err)
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
