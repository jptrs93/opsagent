package webuihandler

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func personalSessionCtx(user *apigen.InternalUser, token string) apigen.Context {
	return apigen.Context{Ctx: context.Background(), User: user, Token: token}
}

func mustVerifyAuth(t *testing.T, h *Handler, token string) (apigen.Context, error) {
	t.Helper()
	r := httptest.NewRequest(http.MethodGet, "/v1/anything", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	policy := apigen.AccessPolicy{PolicyType: apigen.AccessPolicyType_ANY_OF, Scopes: []string{"default"}}
	return h.VerifyAuth(context.Background(), httptest.NewRecorder(), r, policy)
}

func TestPersonalSessionLoginListRevoke(t *testing.T) {
	h, user := newAuthTestHandler(t)

	resp, err := h.startPersonalSession(bg(), user)
	if err != nil {
		t.Fatalf("startPersonalSession: %v", err)
	}
	if resp.Token == "" {
		t.Fatal("login response carries no token")
	}

	verified, err := mustVerifyAuth(t, h, resp.Token)
	if err != nil {
		t.Fatalf("VerifyAuth on personal token: %v", err)
	}
	if verified.User.Delegated {
		t.Fatal("personal session marked delegated")
	}

	list, err := h.PostV1PersonalSessionsList(personalSessionCtx(user, resp.Token), &apigen.EmptyRequest{})
	if err != nil {
		t.Fatalf("PostV1PersonalSessionsList: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(list.Items))
	}
	session := list.Items[0]
	if !session.Current {
		t.Fatal("calling session not marked current")
	}
	if session.ExpiresAt.IsZero() || session.CreatedAt.IsZero() || session.LastActiveAt.IsZero() {
		t.Fatalf("session timestamps not populated: %#v", session)
	}

	if err := h.PostV1PersonalSessionsRevoke(personalSessionCtx(user, resp.Token), &apigen.PersonalSessionRevokeRequest{ID: session.ID}); err != nil {
		t.Fatalf("PostV1PersonalSessionsRevoke: %v", err)
	}
	if _, err := mustVerifyAuth(t, h, resp.Token); !errors.Is(err, InvalidAuthTokenErr) {
		t.Fatalf("VerifyAuth after revoke = %v, want InvalidAuthTokenErr", err)
	}
	if err := h.PostV1PersonalSessionsRevoke(personalSessionCtx(user, resp.Token), &apigen.PersonalSessionRevokeRequest{ID: session.ID}); err != nil {
		t.Fatalf("second revoke = %v, want nil", err)
	}
}

func TestPersonalSessionRevokeForeignID(t *testing.T) {
	h, user := newAuthTestHandler(t)
	resp, err := h.startPersonalSession(bg(), user)
	if err != nil {
		t.Fatalf("startPersonalSession: %v", err)
	}
	list, err := h.PostV1PersonalSessionsList(personalSessionCtx(user, resp.Token), &apigen.EmptyRequest{})
	if err != nil {
		t.Fatalf("PostV1PersonalSessionsList: %v", err)
	}

	other := &apigen.InternalUser{ID: 2, Name: "other"}
	h.Store.WriteUser(other)
	otherCtx := personalSessionCtx(other, h.mustToken(t, other.ID, []string{"default"}, time.Hour))

	if err := h.PostV1PersonalSessionsRevoke(otherCtx, &apigen.PersonalSessionRevokeRequest{ID: list.Items[0].ID}); !errors.Is(err, PersonalSessionNotFoundErr) {
		t.Fatalf("foreign revoke = %v, want PersonalSessionNotFoundErr", err)
	}
	if err := h.PostV1PersonalSessionsRevoke(otherCtx, &apigen.PersonalSessionRevokeRequest{ID: "missing"}); !errors.Is(err, PersonalSessionNotFoundErr) {
		t.Fatalf("missing revoke = %v, want PersonalSessionNotFoundErr", err)
	}
}

func TestSidlessTokenKeepsStatelessPath(t *testing.T) {
	h, user := newAuthTestHandler(t)
	token := h.mustToken(t, user.ID, []string{"default"}, time.Hour)
	if _, err := mustVerifyAuth(t, h, token); err != nil {
		t.Fatalf("VerifyAuth on sid-less token: %v", err)
	}
	list, err := h.PostV1PersonalSessionsList(personalSessionCtx(user, token), &apigen.EmptyRequest{})
	if err != nil {
		t.Fatalf("PostV1PersonalSessionsList: %v", err)
	}
	if len(list.Items) != 0 {
		t.Fatalf("len(items) = %d, want 0", len(list.Items))
	}
}
