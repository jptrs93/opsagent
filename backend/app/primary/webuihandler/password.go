package webuihandler

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/util/jwtu"
)

// Password login is an opt-in alternative to passkeys for installs where a
// browser will not run WebAuthn at all: plain HTTP on anything other than
// localhost, or HTTPS behind a certificate the browser does not trust. It is
// gated by ClusterSettings.Auth.PasswordLoginEnabled so a production install
// that never turns it on exposes no password surface.

const (
	passwordMinLength = 8
	passwordMaxLength = 256
)

var (
	PasswordLoginDisabledErr = apigen.NewApiErr("Password login is not enabled", "password_login_disabled", http.StatusForbidden)
	InvalidPasswordLoginErr  = apigen.NewApiErr("Invalid username or password", "invalid_credentials", http.StatusUnauthorized)
	PasswordTooShortErr      = apigen.NewApiErr(fmt.Sprintf("Password must be at least %d characters", passwordMinLength), "password_too_short", http.StatusBadRequest)
	PasswordTooLongErr       = apigen.NewApiErr(fmt.Sprintf("Password must be at most %d characters", passwordMaxLength), "password_too_long", http.StatusBadRequest)
)

// dummyPasswordHash gives a login attempt for an unknown username the same
// Argon2id cost as one for a known user, so response timing does not reveal
// which usernames exist.
var (
	dummyPasswordHashOnce sync.Once
	dummyPasswordHash     string
)

func loadDummyPasswordHash() string {
	dummyPasswordHashOnce.Do(func() {
		hash, err := authu.HashPassword("opendeploy-dummy-password")
		if err != nil {
			panic(fmt.Sprintf("hashing dummy password: %v", err))
		}
		dummyPasswordHash = hash
	})
	return dummyPasswordHash
}

func (h *Handler) passwordLoginEnabled() bool {
	settings := h.ConfigService.Snapshot().Settings
	return h.ConfigService.MustLoadConfigBoolValue(settings.Auth.PasswordLoginEnabled)
}

// GetV1AuthMethods is unauthenticated: the login page has to know which
// controls to render before anyone has a token.
func (h *Handler) GetV1AuthMethods(ctx apigen.Context) (*apigen.AuthMethodsResponse, error) {
	return &apigen.AuthMethodsResponse{
		PasskeyLoginEnabled:  true,
		PasswordLoginEnabled: h.passwordLoginEnabled(),
	}, nil
}

func (h *Handler) PostV1AuthPasswordLogin(ctx apigen.Context, req *apigen.PasswordLoginRequest) (*apigen.LoginResponse, error) {
	if !h.passwordLoginEnabled() {
		return nil, PasswordLoginDisabledErr
	}
	username := strings.TrimSpace(req.Username)
	if username == "" || req.Password == "" {
		return nil, InvalidPasswordLoginErr
	}
	// Names are compared trimmed on both sides: the master-password handler
	// now trims on creation, but users created before it did may carry
	// surrounding whitespace and must still be able to log in.
	user, err := h.Store.FetchUserMatching(func(u *apigen.InternalUser) bool {
		return strings.TrimSpace(u.Name) == username
	})
	hash := loadDummyPasswordHash()
	known := false
	if err == nil && user.PasswordHash != "" {
		hash = user.PasswordHash
		known = true
	} else if err != nil && !errors.Is(err, state.ErrNotFound) {
		return nil, err
	}
	ok, err := authu.VerifyPassword(req.Password, hash)
	if err != nil {
		return nil, fmt.Errorf("verifying password: %w", err)
	}
	if !ok || !known {
		return nil, InvalidPasswordLoginErr
	}
	return h.startPersonalSession(ctx, user)
}

// PostV1AuthPasswordSet sets or replaces the caller's own password. It accepts
// the bootstrap scope so first-time setup can finish with a password instead
// of a passkey, and in that case it ends in a full session like passkey
// registration does. A caller that already holds a default session gets that
// same session echoed back rather than a second one.
func (h *Handler) PostV1AuthPasswordSet(ctx apigen.Context, req *apigen.PasswordSetRequest) (*apigen.LoginResponse, error) {
	if err := requireHuman(ctx); err != nil {
		return nil, err
	}
	if !h.passwordLoginEnabled() {
		return nil, PasswordLoginDisabledErr
	}
	if err := validatePassword(req.Password); err != nil {
		return nil, err
	}
	hash, err := authu.HashPassword(req.Password)
	if err != nil {
		return nil, fmt.Errorf("hashing password: %w", err)
	}
	userID := ctx.User.ID
	h.Store.UpdateUserMatching(func(u *apigen.InternalUser) bool { return u.ID == userID }, func(u *apigen.InternalUser) {
		u.PasswordHash = hash
	})
	user, err := h.Store.FetchUserByID(userID)
	if err != nil {
		return nil, err
	}
	if claims, _, err := h.jwtAuth.VerifyAndResolveUser(ctx.Token); err == nil {
		scopes := jwtu.ScopesFromClaims(claims)
		if slices.Contains(scopes, ScopeDefault) {
			expiry, err := jwtu.ExpiryFromClaims(claims)
			if err != nil {
				return nil, err
			}
			return newLoginResponse(user, ctx.Token, scopes, expiry), nil
		}
	}
	return h.startPersonalSession(ctx, user)
}

func validatePassword(password string) error {
	n := utf8.RuneCountInString(password)
	if n < passwordMinLength {
		return PasswordTooShortErr
	}
	if n > passwordMaxLength {
		return PasswordTooLongErr
	}
	return nil
}
