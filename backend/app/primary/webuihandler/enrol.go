package webuihandler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// Enrolment and recovery: a root session (the master password) or an
// operator with the right grants creates users with their privileges, hands
// over a single-use link, or opens a session for a user directly.

// linkSessionTTL is how long an enrolment or recovery link stays
// redeemable.
const linkSessionTTL = 7 * 24 * time.Hour

var (
	UserNameTakenErr  = apigen.NewApiErr("Username already in use", "user_name_taken", http.StatusConflict)
	UserNotFoundErr   = apigen.NewApiErr("User not found", "user_not_found", http.StatusNotFound)
	LinkInvalidErr    = apigen.NewApiErr("This link is not valid any more", "link_invalid", http.StatusUnauthorized)
	RootOnlyErr       = apigen.NewApiErr("Only the master password can open a session for a user", "root_only", http.StatusForbidden)
	PasswordWeakErr   = apigen.NewApiErr("Password must be at least 8 characters", "password_too_short", http.StatusBadRequest)
	minPasswordLength = 8
)

func isRoot(ctx apigen.Context) bool {
	return ctx.User == nil && ctx.SessionKind == apigen.UserSessionKind_USER_SESSION_KIND_ROOT
}

// requireEnrolAccess admits a root session, or a full session holding every
// listed verb on its entity kind.
func (h *Handler) requireEnrolAccess(ctx apigen.Context, needs ...struct {
	verb   apigen.AuthzVerb
	entity apigen.AuthzEntityKind
}) error {
	if isRoot(ctx) {
		return nil
	}
	if err := requireHuman(ctx); err != nil {
		return err
	}
	for _, n := range needs {
		if err := h.requireAccess(ctx, n.verb, n.entity, 0, 0); err != nil {
			return err
		}
	}
	return nil
}

type need = struct {
	verb   apigen.AuthzVerb
	entity apigen.AuthzEntityKind
}

func hashPassword(password string) (string, error) {
	if len(password) < minPasswordLength {
		return "", PasswordWeakErr
	}
	hash, err := authu.HashPassword(password)
	if err != nil {
		return "", fmt.Errorf("hashing password: %w", err)
	}
	return hash, nil
}

// PostV1UsersCreate writes a pending user, its password hash when one was
// chosen, and its grants in one commit. Root writes as author 0.
func (h *Handler) PostV1UsersCreate(ctx apigen.Context, req *apigen.UserCreateRequest) (*apigen.CoreWriteUpdate, error) {
	if err := h.requireEnrolAccess(ctx, need{vCreate, eUser}, need{vCreate, eAccess}); err != nil {
		return nil, err
	}
	user := &apigen.User{}
	if req.Name.Present {
		name := strings.TrimSpace(req.Name.Value)
		if name == "" {
			return nil, UsernameRequiredErr
		}
		user.Name = apigen.Some(name)
	}
	if req.Password.Present {
		hash, err := hashPassword(req.Password.Value)
		if err != nil {
			return nil, err
		}
		user.PasswordHash = apigen.Some(hash)
	}
	webAuthNID, err := authu.GenerateWebAuthnID(32)
	if err != nil {
		return nil, err
	}
	user.Authentication = apigen.UserAuthentication{WebAuthnID: webAuthNID}
	author := requestUserID(ctx)
	var userM pq.Mutation
	_, err = h.Authz.CreateGrantsWithin(req.Grants, author, func(build authz.GrantMutations) error {
		var err error
		userM, err = users.Create(h.Store, user, author, build)
		return err
	})
	switch {
	case errors.Is(err, users.ErrNameTaken):
		return nil, UserNameTakenErr
	case err != nil:
		return nil, mapAuthzErr(err)
	}
	slog.InfoContext(ctx, "created user", "user", user.ID, "grants", len(req.Grants))
	return h.writtenToWriter(ctx, userM), nil
}

// PostV1UsersLink issues a single-use link that signs in as the user. The
// link row records the issuer as author and the issuer's address.
func (h *Handler) PostV1UsersLink(ctx apigen.Context, req *apigen.UserLinkRequest) (*apigen.UserLinkResponse, error) {
	if err := h.requireEnrolAccess(ctx, need{vCreate, eUser}, need{vCreate, eAccess}); err != nil {
		return nil, err
	}
	user, err := users.ByID(h.Store.Queries(), req.UserID)
	if errors.Is(err, users.ErrNotFound) {
		return nil, UserNotFoundErr
	}
	if err != nil {
		return nil, err
	}
	rec, token, err := newSessionRecord(ctx, linkTokenKind, apigen.UserSessionKind_USER_SESSION_KIND_LINK, linkSessionTTL)
	if err != nil {
		return nil, err
	}
	rec.UserID = user.ID
	rec.Author = requestUserID(ctx)
	rec.IssuedBy = uint64(rec.Author)
	if err := users.InsertUserSession(h.Store, rec); err != nil {
		return nil, fmt.Errorf("storing link: %w", err)
	}
	slog.InfoContext(ctx, "issued sign-in link", "session", rec.ID, "user", user.ID)
	return &apigen.UserLinkResponse{Token: token, SessionID: rec.ID, ExpiresAt: rec.ExpiresAt}, nil
}

// PostV1AuthLinkRedeem turns a link into a full session for its user,
// once. Every failure is the same error so the response does not say
// whether the id exists.
func (h *Handler) PostV1AuthLinkRedeem(ctx apigen.Context, req *apigen.LinkRedeemRequest) (*apigen.LinkRedeemResponse, error) {
	token := strings.TrimSpace(req.Token)
	kind, linkID, ok := splitToken(token)
	if !ok || kind != linkTokenKind {
		return nil, LinkInvalidErr
	}
	open, sessionToken, err := newSessionRecord(ctx, userTokenKind, apigen.UserSessionKind_USER_SESSION_KIND_FULL, defaultSessionTokenTTL)
	if err != nil {
		return nil, err
	}
	opened, err := users.RedeemLink(h.Store, linkID, hashToken(token), open, time.Now())
	if errors.Is(err, users.ErrLinkInvalid) {
		slog.InfoContext(ctx, "link redemption refused", "session", linkID)
		return nil, LinkInvalidErr
	}
	if err != nil {
		return nil, fmt.Errorf("redeeming link: %w", err)
	}
	user, err := users.ByID(h.Store.Queries(), opened.UserID)
	if err != nil {
		return nil, fmt.Errorf("fetching redeemed user: %w", err)
	}
	slog.InfoContext(ctx, fmt.Sprintf("redeemed sign-in link address=%s", opened.RequestingAddress), "session", opened.ID, "link", linkID, "user", user.ID)
	return &apigen.LinkRedeemResponse{
		Session:     *newLoginResponse(user, sessionToken, opened.Kind, opened.ExpiresAt, opened.ID),
		NameSet:     user.Name.Present,
		PasswordSet: user.PasswordHash.Present,
	}, nil
}

// PostV1AuthOpenSession lets a root session sign in as a user without that
// user's credential: the master password standing in for it, as it always
// has for first-time setup and recovery.
func (h *Handler) PostV1AuthOpenSession(ctx apigen.Context, req *apigen.OpenSessionRequest) (*apigen.LoginResponse, error) {
	if !isRoot(ctx) {
		return nil, RootOnlyErr
	}
	user, err := users.ByID(h.Store.Queries(), req.UserID)
	if errors.Is(err, users.ErrNotFound) {
		return nil, UserNotFoundErr
	}
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "root opened session for user", "user", user.ID)
	return h.startDefaultUserSession(ctx, user)
}
