package webuihandler

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
)

func (h *Handler) rootCtx(t *testing.T) apigen.Context {
	t.Helper()
	res, err := h.PostV1AuthMaster(bg(), &apigen.MasterPasswordRequest{Password: testMasterPassword})
	if err != nil {
		t.Fatalf("PostV1AuthMaster: %v", err)
	}
	ctx, err := h.verifyToken(res.Token)
	if err != nil {
		t.Fatalf("verify root token: %v", err)
	}
	return ctx
}

func adminGrantSource() apigen.AuthzGrantSource {
	return apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{Template: &apigen.AuthzTemplateGrant{TemplateID: authz.ClusterAdminTemplateID}}}
}

func createdUserID(t *testing.T, update *apigen.CoreWriteUpdate) uint64 {
	t.Helper()
	for _, m := range update.Mutations {
		if m.Type() == apigen.CoreEntityType_CORE_ENTITY_USER {
			return m.EntityID()
		}
	}
	t.Fatalf("receipt carries no user mutation: %+v", update)
	return 0
}

func TestRootCreatesUserWithGrantsInOneCommit(t *testing.T) {
	h, _ := newAuthTestHandler(t)
	setupAuth(t, h, nil)
	root := h.rootCtx(t)

	sub, unsub := h.Store.SubscribeUpdates()
	defer unsub()
	update, err := h.PostV1UsersCreate(root, &apigen.UserCreateRequest{Name: apigen.Some(" alice "), Password: apigen.Some(testUserPassword), Grants: []apigen.AuthzGrantSource{adminGrantSource()}})
	if err != nil {
		t.Fatalf("PostV1UsersCreate: %v", err)
	}
	id := createdUserID(t, update)
	published := <-sub
	if len(published.Mutations) != 2 || published.Mutations[0].Type() != apigen.CoreEntityType_CORE_ENTITY_USER || published.Mutations[1].Type() != apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT {
		t.Fatalf("one commit must carry the user and its grant: %+v", published.Mutations)
	}
	if published.Actor != 0 {
		t.Fatalf("root writes as author 0, got %d", published.Actor)
	}
	created, err := users.ByID(h.Store.Queries(), id)
	if err != nil || created.Name.Value != "alice" || !created.PasswordHash.Present || created.ActivatedAt.Present {
		t.Fatalf("created user = %+v, %v; want named, with a password, pending", created, err)
	}
	if grants := h.Authz.GrantsForUser(id); len(grants) != 1 || grants[0].Grant.Value.Template.TemplateID != authz.ClusterAdminTemplateID {
		t.Fatalf("grants = %+v", grants)
	}
	// The receipt never carries the hash.
	for _, m := range update.Mutations {
		if u := m.Entity().Value.User; u != nil && u.PasswordHash.Present {
			t.Fatal("receipt leaked the password hash")
		}
	}

	// The name is taken now, trimmed.
	if _, err := h.PostV1UsersCreate(root, &apigen.UserCreateRequest{Name: apigen.Some("alice ")}); !errors.Is(err, UserNameTakenErr) {
		t.Fatalf("duplicate name err = %v", err)
	}
	if _, err := h.PostV1UsersCreate(root, &apigen.UserCreateRequest{Password: apigen.Some("short")}); !errors.Is(err, PasswordWeakErr) {
		t.Fatalf("short password err = %v", err)
	}
	// A bad grant fails the whole request and leaves no user behind.
	before, _ := h.Queries.ListUserRows(root)
	if _, err := h.PostV1UsersCreate(root, &apigen.UserCreateRequest{Name: apigen.Some("bob"), Grants: []apigen.AuthzGrantSource{{Value: apigen.AuthzGrantSourceValueOneof{Template: &apigen.AuthzTemplateGrant{TemplateID: 999}}}}}); err == nil {
		t.Fatal("unknown template accepted")
	}
	if after, _ := h.Queries.ListUserRows(root); len(after) != len(before) {
		t.Fatalf("users after failed create = %d, want %d", len(after), len(before))
	}

	// Password login works for the new user and activates it.
	res, err := h.PostV1AuthPasswordLogin(bg(), &apigen.PasswordLoginRequest{Username: "alice", Password: testUserPassword})
	if err != nil || res.UserID != id {
		t.Fatalf("login as alice = %+v, %v", res, err)
	}
	if activated, _ := users.ByID(h.Store.Queries(), id); !activated.ActivatedAt.Present {
		t.Fatal("login did not activate the user")
	}
}

func TestEnrolRoutesNeedRootOrGrants(t *testing.T) {
	h, admin := newAuthTestHandler(t)
	setupAuth(t, h, admin)
	if _, err := h.Authz.CreateGrant(&apigen.AuthzGrant{UserID: admin.ID, Grant: adminGrantSource()}, 0); err != nil {
		t.Fatal(err)
	}
	users.Write(h.Store, &apigen.User{ID: 2, Name: apigen.Some("plain")})
	plain := h.operatorCtx(t, &apigen.User{ID: 2})
	adminCtx := h.operatorCtx(t, admin)

	if _, err := h.PostV1UsersCreate(plain, &apigen.UserCreateRequest{Name: apigen.Some("x")}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("plain user create err = %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1UsersLink(plain, &apigen.UserLinkRequest{UserID: admin.ID}); !errors.Is(err, AccessDeniedErr) {
		t.Fatalf("plain user link err = %v, want AccessDeniedErr", err)
	}
	if _, err := h.PostV1AuthOpenSession(adminCtx, &apigen.OpenSessionRequest{UserID: 2}); !errors.Is(err, RootOnlyErr) {
		t.Fatalf("admin open-session err = %v, want RootOnlyErr", err)
	}
	update, err := h.PostV1UsersCreate(adminCtx, &apigen.UserCreateRequest{Name: apigen.Some("carol"), Grants: []apigen.AuthzGrantSource{adminGrantSource()}})
	if err != nil {
		t.Fatalf("admin create: %v", err)
	}
	if update.Actor != int64(admin.ID) {
		t.Fatalf("admin writes as itself, got actor %d", update.Actor)
	}
	if _, err := h.PostV1UsersLink(adminCtx, &apigen.UserLinkRequest{UserID: createdUserID(t, update)}); err != nil {
		t.Fatalf("admin link: %v", err)
	}
}

func TestLinkIssueNeedsAccessCreate(t *testing.T) {
	h, admin := newAuthTestHandler(t)
	setupAuth(t, h, admin)
	if _, err := h.Authz.CreateGrant(clusterAdminGrant(admin.ID), 0); err != nil {
		t.Fatal(err)
	}
	users.Write(h.Store, &apigen.User{ID: 2, Name: apigen.Some("helpdesk")})
	users.Write(h.Store, &apigen.User{ID: 3, Name: apigen.Some("plain")})
	if _, err := h.Authz.CreateGrant(ruleGrant(2, allowRule(apigen.AuthzSelector{
		Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_CREATE, apigen.AuthzVerb_AUTHZ_VERB_UPDATE, apigen.AuthzVerb_AUTHZ_VERB_DELETE, apigen.AuthzVerb_AUTHZ_VERB_VIEW),
		Spaces:      exactSpaces(0),
		EntityTypes: exactEntityTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER),
		EntityRefs:  allEntityRefs(),
	})), 0); err != nil {
		t.Fatal(err)
	}
	helpdesk := h.operatorCtx(t, &apigen.User{ID: 2})
	for _, target := range []uint64{admin.ID, 3} {
		if _, err := h.PostV1UsersLink(helpdesk, &apigen.UserLinkRequest{UserID: target}); !errors.Is(err, AccessDeniedErr) {
			t.Fatalf("link for user %d with every user verb and no access:create: err = %v, want AccessDeniedErr", target, err)
		}
	}
	if _, err := h.PostV1UsersLink(h.operatorCtx(t, admin), &apigen.UserLinkRequest{UserID: 3}); err != nil {
		t.Fatalf("admin link: %v", err)
	}
}

func TestLinkRedeemsOnce(t *testing.T) {
	h, _ := newAuthTestHandler(t)
	setupAuth(t, h, nil)
	root := h.rootCtx(t)
	update, err := h.PostV1UsersCreate(root, &apigen.UserCreateRequest{Grants: []apigen.AuthzGrantSource{adminGrantSource()}})
	if err != nil {
		t.Fatal(err)
	}
	id := createdUserID(t, update)

	link, err := h.PostV1UsersLink(root, &apigen.UserLinkRequest{UserID: id})
	if err != nil {
		t.Fatalf("PostV1UsersLink: %v", err)
	}
	if !strings.HasPrefix(link.Token, linkTokenKind) {
		t.Fatalf("token %q does not carry the link prefix", link.Token)
	}
	if until := time.Until(link.ExpiresAt); until > linkSessionTTL || until < linkSessionTTL-time.Minute {
		t.Fatalf("expiry in %v, want about %v", until, linkSessionTTL)
	}
	if _, err := h.verifyToken(link.Token); !errors.Is(err, InvalidAuthTokenErr) {
		t.Fatalf("link token as bearer err = %v, want InvalidAuthTokenErr", err)
	}
	for name, token := range map[string]string{
		"garbage":      "nonsense",
		"wrong prefix": "u_" + strings.TrimPrefix(link.Token, linkTokenKind),
		"wrong secret": link.Token[:len(link.Token)-4] + "zzzz",
	} {
		if _, err := h.PostV1AuthLinkRedeem(bg(), &apigen.LinkRedeemRequest{Token: token}); !errors.Is(err, LinkInvalidErr) {
			t.Fatalf("%s: err = %v, want LinkInvalidErr", name, err)
		}
	}

	redeemed, err := h.PostV1AuthLinkRedeem(bg(), &apigen.LinkRedeemRequest{Token: link.Token})
	if err != nil {
		t.Fatalf("PostV1AuthLinkRedeem: %v", err)
	}
	if redeemed.Session.UserID != id || redeemed.Session.Kind != fullSession || redeemed.NameSet || redeemed.PasswordSet {
		t.Fatalf("redeemed = %+v", redeemed)
	}
	ctx, err := h.verifyToken(redeemed.Session.Token)
	if err != nil || ctx.User == nil || ctx.User.ID != id {
		t.Fatalf("redeemed session does not verify: %+v, %v", ctx, err)
	}
	if user, _ := users.ByID(h.Store.Queries(), id); !user.ActivatedAt.Present {
		t.Fatal("redemption did not activate the user")
	}
	opened, err := users.UserSessionByID(h.Store.Queries(), redeemed.Session.SessionID)
	if err != nil {
		t.Fatal(err)
	}
	linkRow, err := users.UserSessionByID(h.Store.Queries(), link.SessionID)
	if err != nil || linkRow.RedeemedAt.IsZero() || opened.OpenedBy != linkRow.EntityID {
		t.Fatalf("link row = %+v, opened = %+v, %v", linkRow, opened, err)
	}
	if _, err := h.PostV1AuthLinkRedeem(bg(), &apigen.LinkRedeemRequest{Token: link.Token}); !errors.Is(err, LinkInvalidErr) {
		t.Fatalf("second redemption err = %v, want LinkInvalidErr", err)
	}

	// The landing page then names the user and sets a password.
	if _, err := h.PostV1AccountRename(ctx, &apigen.AccountRenameRequest{Name: "dave"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if err := h.PostV1AccountPassword(ctx, &apigen.AccountPasswordRequest{Next: testUserPassword}); err != nil {
		t.Fatalf("set password: %v", err)
	}
	if _, err := h.PostV1AuthPasswordLogin(bg(), &apigen.PasswordLoginRequest{Username: "dave", Password: testUserPassword}); err != nil {
		t.Fatalf("login after landing: %v", err)
	}

	// A revoked link is refused; an expired one too.
	revokable, _ := h.PostV1UsersLink(root, &apigen.UserLinkRequest{UserID: id})
	if _, err := users.RevokeUserSession(h.Store, revokable.SessionID, id, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostV1AuthLinkRedeem(bg(), &apigen.LinkRedeemRequest{Token: revokable.Token}); !errors.Is(err, LinkInvalidErr) {
		t.Fatalf("revoked link err = %v", err)
	}
	expiredRec, expiredToken, err := newSessionRecord(bg(), linkTokenKind, linkSession, -time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	expiredRec.UserID = id
	if err := users.InsertUserSession(h.Store, expiredRec); err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostV1AuthLinkRedeem(bg(), &apigen.LinkRedeemRequest{Token: expiredToken}); !errors.Is(err, LinkInvalidErr) {
		t.Fatalf("expired link err = %v", err)
	}
}

func TestIssuerSeesAndCancelsItsLinks(t *testing.T) {
	h, admin := newAuthTestHandler(t)
	setupAuth(t, h, admin)
	if _, err := h.Authz.CreateGrant(&apigen.AuthzGrant{UserID: admin.ID, Grant: adminGrantSource()}, 0); err != nil {
		t.Fatal(err)
	}
	adminCtx := h.operatorCtx(t, admin)
	users.Write(h.Store, &apigen.User{ID: 2, Name: apigen.Some("target")})
	link, err := h.PostV1UsersLink(adminCtx, &apigen.UserLinkRequest{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	list, err := h.PostV1UserSessionsList(adminCtx)
	if err != nil {
		t.Fatal(err)
	}
	var found bool
	for _, s := range list.Items {
		if s.SessionID == link.SessionID && s.Kind == linkSession && s.UserID.Value == 2 {
			found = true
		}
	}
	if !found {
		t.Fatalf("issuer's list lacks the link: %+v", list.Items)
	}
	// The issuer's stream carries the link; the target's does too; a
	// bystander's does not.
	users.Write(h.Store, &apigen.User{ID: 3, Name: apigen.Some("bystander")})
	for viewer, want := range map[uint64]bool{admin.ID: true, 2: true, 3: false} {
		opening, err := h.PostV1GlobalEvents(enforceCtx(viewer, false), &apigen.EventStreamRequest{})
		if err != nil {
			t.Fatal(err)
		}
		var got bool
		for _, e := range foldMsg(opening, apigen.CoreEntityType_CORE_ENTITY_USER_SESSION) {
			if e.Value.UserSession.SessionID == link.SessionID {
				got = true
			}
		}
		if got != want {
			t.Fatalf("viewer %d sees link = %v, want %v", viewer, got, want)
		}
	}
	// A redeemed link stays in the issuer's list and stream even though
	// the redeem is authored by the target.
	redeemed, err := h.PostV1UsersLink(adminCtx, &apigen.UserLinkRequest{UserID: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := h.PostV1AuthLinkRedeem(bg(), &apigen.LinkRedeemRequest{Token: redeemed.Token}); err != nil {
		t.Fatal(err)
	}
	list, err = h.PostV1UserSessionsList(adminCtx)
	if err != nil {
		t.Fatal(err)
	}
	found = false
	for _, s := range list.Items {
		if s.SessionID == redeemed.SessionID && s.RedeemedAt.Present && s.IssuedBy.Value == admin.ID {
			found = true
		}
	}
	if !found {
		t.Fatalf("issuer's list lost the redeemed link: %+v", list.Items)
	}
	if err := h.PostV1UserSessionsRevoke(adminCtx, &apigen.UserSessionRevokeRequest{SessionID: link.SessionID}); err != nil {
		t.Fatalf("revoke link: %v", err)
	}
	if _, err := h.PostV1AuthLinkRedeem(bg(), &apigen.LinkRedeemRequest{Token: link.Token}); !errors.Is(err, LinkInvalidErr) {
		t.Fatalf("cancelled link err = %v", err)
	}
	// Another user may not revoke it.
	other := h.operatorCtx(t, &apigen.User{ID: 3})
	if err := h.PostV1UserSessionsRevoke(other, &apigen.UserSessionRevokeRequest{SessionID: link.SessionID}); !errors.Is(err, UserSessionNotFoundErr) {
		t.Fatalf("bystander revoke err = %v", err)
	}
}

func TestRootOpensSessionForUser(t *testing.T) {
	h, _ := newAuthTestHandler(t)
	setupAuth(t, h, nil)
	root := h.rootCtx(t)
	update, err := h.PostV1UsersCreate(root, &apigen.UserCreateRequest{Name: apigen.Some("erin")})
	if err != nil {
		t.Fatal(err)
	}
	id := createdUserID(t, update)
	res, err := h.PostV1AuthOpenSession(root, &apigen.OpenSessionRequest{UserID: id})
	if err != nil || res.UserID != id || res.Kind != fullSession || res.Name != "erin" {
		t.Fatalf("open session = %+v, %v", res, err)
	}
	if ctx, err := h.verifyToken(res.Token); err != nil || ctx.User.ID != id {
		t.Fatalf("opened session does not verify: %v", err)
	}
	if user, _ := users.ByID(h.Store.Queries(), id); !user.ActivatedAt.Present {
		t.Fatal("open-session did not activate the user")
	}
	if _, err := h.PostV1AuthOpenSession(root, &apigen.OpenSessionRequest{UserID: 999}); !errors.Is(err, UserNotFoundErr) {
		t.Fatalf("unknown user err = %v", err)
	}
}

func TestAccountRoutesKeepAWayIn(t *testing.T) {
	h, user := newAuthTestHandler(t)
	setupAuth(t, h, user)
	ctx := h.operatorCtx(t, user)

	view, err := h.GetV1Account(ctx)
	if err != nil || view.UserID != user.ID || !view.HasPassword || view.Name.Value != "operator" || len(view.Passkeys) != 0 {
		t.Fatalf("account = %+v, %v", view, err)
	}
	// Clearing the only password is refused; changing it needs the current
	// one.
	if err := h.PostV1AccountPassword(ctx, &apigen.AccountPasswordRequest{Current: apigen.Some(testUserPassword), Next: ""}); !errors.Is(err, LastWayInErr) {
		t.Fatalf("clear last password err = %v", err)
	}
	if err := h.PostV1AccountPassword(ctx, &apigen.AccountPasswordRequest{Next: "another password"}); !errors.Is(err, CurrentPasswordRequiredErr) {
		t.Fatalf("change without current err = %v", err)
	}
	if err := h.PostV1AccountPassword(ctx, &apigen.AccountPasswordRequest{Current: apigen.Some("wrong"), Next: "another password"}); !errors.Is(err, CurrentPasswordWrongErr) {
		t.Fatalf("change with wrong current err = %v", err)
	}
	if err := h.PostV1AccountPassword(ctx, &apigen.AccountPasswordRequest{Current: apigen.Some(testUserPassword), Next: "another password"}); err != nil {
		t.Fatalf("change password: %v", err)
	}
	if _, err := h.PostV1AuthPasswordLogin(bg(), &apigen.PasswordLoginRequest{Username: "operator", Password: "another password"}); err != nil {
		t.Fatalf("login with the new password: %v", err)
	}

	// With a passkey, the password can go; then the passkey cannot.
	users.UpdateMatching(h.Store, func(u *apigen.User) bool { return u.ID == user.ID }, func(u *apigen.User) {
		users.SetCredential(u, []byte("cred"), []byte("{}"), time.Now())
	})
	if view, _ := h.GetV1Account(ctx); len(view.Passkeys) != 1 || view.Passkeys[0].Label != "Passkey" {
		t.Fatalf("account passkeys = %+v", view.Passkeys)
	}
	if err := h.PostV1AccountPassword(ctx, &apigen.AccountPasswordRequest{Current: apigen.Some("another password"), Next: ""}); err != nil {
		t.Fatalf("clear password with a passkey: %v", err)
	}
	if err := h.PostV1AccountPasskeysDelete(ctx, &apigen.AccountPasskeyDeleteRequest{ID: []byte("cred")}); !errors.Is(err, LastWayInErr) {
		t.Fatalf("delete last passkey err = %v", err)
	}
	if err := h.PostV1AccountPasskeysDelete(ctx, &apigen.AccountPasskeyDeleteRequest{ID: []byte("missing")}); !errors.Is(err, PasskeyNotFoundErr) {
		t.Fatalf("delete missing passkey err = %v", err)
	}
	if err := h.PostV1AccountPassword(ctx, &apigen.AccountPasswordRequest{Next: testUserPassword}); err != nil {
		t.Fatalf("set password without current when none exists: %v", err)
	}
	if err := h.PostV1AccountPasskeysDelete(ctx, &apigen.AccountPasskeyDeleteRequest{ID: []byte("cred")}); err != nil {
		t.Fatalf("delete passkey with a password: %v", err)
	}

	// Rename refuses a taken name, trimmed.
	users.Write(h.Store, &apigen.User{ID: 2, Name: apigen.Some("taken")})
	if _, err := h.PostV1AccountRename(ctx, &apigen.AccountRenameRequest{Name: " taken "}); !errors.Is(err, UserNameTakenErr) {
		t.Fatalf("rename to taken err = %v", err)
	}
	if _, err := h.PostV1AccountRename(ctx, &apigen.AccountRenameRequest{Name: "renamed"}); err != nil {
		t.Fatalf("rename: %v", err)
	}
	if view, _ := h.GetV1Account(ctx); view.Name.Value != "renamed" {
		t.Fatalf("account after rename = %+v", view)
	}
}

func TestBrowserEntityStripsPasswordHash(t *testing.T) {
	u := apigen.User{ID: 1, Name: apigen.Some("x"), PasswordHash: apigen.Some("hash"), Authentication: apigen.UserAuthentication{WebAuthnID: []byte{1}}}
	out := browserEntity(apigen.CoreEntity{Value: apigen.CoreEntityValueOneof{User: &u}})
	if got := out.Value.User; got.PasswordHash.Present || len(got.Authentication.WebAuthnID) != 0 || got.Name.Value != "x" {
		t.Fatalf("browser user = %+v", got)
	}
	s := apigen.UserSession{ID: 1, Kind: linkSession, UserID: apigen.Some[uint64](2), TokenHash: apigen.Some([]byte{1})}
	if got := browserEntity(apigen.CoreEntity{Value: apigen.CoreEntityValueOneof{UserSession: &s}}).Value.UserSession; got.TokenHash.Present || got.UserID.Value != 2 {
		t.Fatalf("browser session = %+v", got)
	}
}

func TestActivateExistingMarksSignedInUsers(t *testing.T) {
	h, user := newAuthTestHandler(t)
	users.Write(h.Store, &apigen.User{ID: 2, Name: apigen.Some("never")})
	users.Write(h.Store, &apigen.User{ID: 3, Name: apigen.Some("passkey"), Authentication: apigen.UserAuthentication{Credentials: []apigen.WebAuthnCredential{{ID: []byte("c"), Data: []byte("{}")}}}})
	if _, err := h.startDefaultUserSession(bg(), user); err != nil {
		t.Fatal(err)
	}
	// The session above already activated user 1; strip it again to stand
	// in for a pre-upgrade row.
	if _, err := users.Update(h.Store, user.ID, 0, func(u *apigen.User) error { u.ActivatedAt = apigen.Maybe[time.Time]{}; return nil }); err != nil {
		t.Fatal(err)
	}
	if err := users.ActivateExisting(h.Store); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[uint64]bool{1: true, 2: false, 3: true} {
		if u, _ := users.ByID(h.Store.Queries(), id); u.ActivatedAt.Present != want {
			t.Fatalf("user %d activated = %v, want %v", id, u.ActivatedAt.Present, want)
		}
	}
}
