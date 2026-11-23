package webuihandler

import (
	"errors"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
)

func TestUserSessionLoginListRevoke(t *testing.T) {
	h, user := newAuthTestHandler(t)

	resp, err := h.startDefaultUserSession(bg(), user)
	if err != nil {
		t.Fatalf("startDefaultUserSession: %v", err)
	}
	if !strings.HasPrefix(resp.Token, userTokenKind) {
		t.Fatalf("token %q does not carry the user prefix", resp.Token)
	}
	if resp.Kind != fullSession {
		t.Fatalf("login kind = %v, want FULL", resp.Kind)
	}

	verified, err := h.verifyToken(resp.Token)
	if err != nil {
		t.Fatalf("VerifyAuth on user token: %v", err)
	}
	if verified.Delegated {
		t.Fatal("user session marked delegated")
	}
	if verified.SessionKind != fullSession || verified.SessionExpiresAt.IsZero() {
		t.Fatalf("resolved context = %+v, want a full session with an expiry", verified)
	}

	current, err := h.GetV1AuthCurrentSession(verified)
	if err != nil || current.Token != resp.Token || current.UserID != user.ID || current.Kind != fullSession {
		t.Fatalf("current session = %+v, %v", current, err)
	}

	list, err := h.PostV1UserSessionsList(verified)
	if err != nil {
		t.Fatalf("PostV1UserSessionsList: %v", err)
	}
	if len(list.Items) != 1 {
		t.Fatalf("len(items) = %d, want 1", len(list.Items))
	}
	session := list.Items[0]
	if session.SessionID != current.SessionID || session.UserID.Value != user.ID {
		t.Fatalf("listed session %+v does not match the calling session %s", session, current.SessionID)
	}
	if session.ExpiresAt.IsZero() {
		t.Fatalf("session timestamps not populated: %#v", session)
	}

	if err := h.PostV1UserSessionsRevoke(verified, &apigen.UserSessionRevokeRequest{SessionID: session.SessionID}); err != nil {
		t.Fatalf("PostV1UserSessionsRevoke: %v", err)
	}
	if _, err := h.verifyToken(resp.Token); !errors.Is(err, InvalidAuthTokenErr) {
		t.Fatalf("VerifyAuth after revoke = %v, want InvalidAuthTokenErr", err)
	}
	if err := h.PostV1UserSessionsRevoke(verified, &apigen.UserSessionRevokeRequest{SessionID: session.SessionID}); err != nil {
		t.Fatalf("second revoke = %v, want nil", err)
	}
}

func TestUserSessionRevokeForeignID(t *testing.T) {
	h, user := newAuthTestHandler(t)
	mine := h.operatorCtx(t, user)

	other := &apigen.User{ID: 2, Name: apigen.Some("other")}
	users.Write(h.Store, other)
	otherCtx := h.operatorCtx(t, other)

	if err := h.PostV1UserSessionsRevoke(otherCtx, &apigen.UserSessionRevokeRequest{SessionID: mine.SessionID}); !errors.Is(err, UserSessionNotFoundErr) {
		t.Fatalf("foreign revoke = %v, want UserSessionNotFoundErr", err)
	}
	if err := h.PostV1UserSessionsRevoke(otherCtx, &apigen.UserSessionRevokeRequest{SessionID: "missing"}); !errors.Is(err, UserSessionNotFoundErr) {
		t.Fatalf("missing revoke = %v, want UserSessionNotFoundErr", err)
	}
	if _, err := h.verifyToken(mine.Token); err != nil {
		t.Fatalf("session was revoked by a different user: %v", err)
	}
}
