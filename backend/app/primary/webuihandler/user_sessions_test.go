package webuihandler

import (
	"errors"
	"strings"
	"testing"
	"time"

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
	if session.SessionID != current.SessionID || session.UserID != user.ID {
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

	other := &apigen.User{ID: 2, Name: "other"}
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

// The master password opens a bootstrap session: a real row of the BOOTSTRAP
// kind with a short life, so it is listed and revocable like any other session
// but cannot reach an ordinary route.
func TestMasterPasswordOpensBootstrapSession(t *testing.T) {
	h, user := newAuthTestHandler(t)
	enablePasswordLogin(t, h)

	res, err := h.PostV1AuthMaster(bg(), &apigen.MasterPasswordRequest{Username: user.Name, Password: testMasterPassword})
	if err != nil {
		t.Fatalf("PostV1AuthMaster: %v", err)
	}
	if res.Kind != bootstrapSession {
		t.Fatalf("kind = %v, want BOOTSTRAP", res.Kind)
	}
	if until := time.Until(res.Expiry); until > bootstrapSessionTTL || until < bootstrapSessionTTL-time.Minute {
		t.Fatalf("expiry in %v, want about %v", until, bootstrapSessionTTL)
	}
	ctx, err := h.verifyToken(res.Token)
	if err != nil {
		t.Fatalf("bootstrap token does not verify: %v", err)
	}
	if err := fullSessionPolicy.CanAccess(ctx.SessionKind); err == nil {
		t.Fatal("bootstrap session passed a full-session policy")
	}
	sessions, err := users.ListUserSessions(h.Store.Queries(), user.ID)
	if err != nil || len(sessions) != 1 || sessions[0].Kind != bootstrapSession {
		t.Fatalf("stored sessions = %+v, %v", sessions, err)
	}
}
