package webuihandler

import (
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/jptrs93/goutil/authu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/users"
)

// The account routes are self-service: each acts on the caller's own user
// and nothing else, so none consults authz. The only rule is last-way-in:
// an account keeps at least one of a password and a passkey.

var (
	CurrentPasswordRequiredErr = apigen.NewApiErr("Current password is required", "current_password_required", http.StatusBadRequest)
	CurrentPasswordWrongErr    = apigen.NewApiErr("Current password is wrong", "current_password_wrong", http.StatusForbidden)
	LastWayInErr               = apigen.NewApiErr("You would have no way to sign in; add a passkey or a password first", "last_way_in", http.StatusConflict)
	PasskeyNotFoundErr         = apigen.NewApiErr("Passkey not found", "passkey_not_found", http.StatusNotFound)
)

func accountView(u *apigen.User) *apigen.AccountView {
	view := &apigen.AccountView{UserID: u.ID, Name: u.Name, Email: u.Email, HasPassword: u.PasswordHash.Present, ActivatedAt: u.ActivatedAt}
	for _, c := range u.Authentication.Credentials {
		view.Passkeys = append(view.Passkeys, apigen.AccountPasskey{ID: c.ID, Label: c.Label, CreatedAt: c.CreatedAt, LastUsedAt: c.LastUsedAt})
	}
	return view
}

func (h *Handler) GetV1Account(ctx apigen.Context) (*apigen.AccountView, error) {
	if err := requireHuman(ctx); err != nil {
		return nil, err
	}
	user, err := users.ByID(h.Store.Queries(), ctx.User.ID)
	if err != nil {
		return nil, err
	}
	return accountView(user), nil
}

func (h *Handler) PostV1AccountRename(ctx apigen.Context, req *apigen.AccountRenameRequest) (*apigen.CoreWriteUpdate, error) {
	if err := requireHuman(ctx); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, UsernameRequiredErr
	}
	m, err := users.Rename(h.Store, ctx.User.ID, requestUserID(ctx), name)
	if errors.Is(err, users.ErrNameTaken) {
		return nil, UserNameTakenErr
	}
	if err != nil {
		return nil, err
	}
	slog.InfoContext(ctx, "renamed account", "user", ctx.User.ID)
	return h.writtenToWriter(ctx, m), nil
}

// PostV1AccountPassword sets, changes, or clears the caller's password. The
// current password is required while one exists; clearing needs a passkey
// to remain.
func (h *Handler) PostV1AccountPassword(ctx apigen.Context, req *apigen.AccountPasswordRequest) error {
	if err := requireHuman(ctx); err != nil {
		return err
	}
	var nextHash apigen.Maybe[string]
	if req.Next != "" {
		hash, err := hashPassword(req.Next)
		if err != nil {
			return err
		}
		nextHash = apigen.Some(hash)
	}
	_, err := users.Update(h.Store, ctx.User.ID, requestUserID(ctx), func(u *apigen.User) error {
		if u.PasswordHash.Present {
			if !req.Current.Present {
				return CurrentPasswordRequiredErr
			}
			ok, err := authu.VerifyPassword(req.Current.Value, u.PasswordHash.Value)
			if err != nil {
				return fmt.Errorf("verifying current password: %w", err)
			}
			if !ok {
				return CurrentPasswordWrongErr
			}
		}
		u.PasswordHash = nextHash
		if !users.HasWayIn(u) {
			return LastWayInErr
		}
		return nil
	})
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "changed account password", "user", ctx.User.ID, "cleared", !nextHash.Present)
	return nil
}

func (h *Handler) PostV1AccountPasskeysDelete(ctx apigen.Context, req *apigen.AccountPasskeyDeleteRequest) error {
	if err := requireHuman(ctx); err != nil {
		return err
	}
	_, err := users.Update(h.Store, ctx.User.ID, requestUserID(ctx), func(u *apigen.User) error {
		if !users.RemoveCredential(u, req.ID) {
			return PasskeyNotFoundErr
		}
		if !users.HasWayIn(u) {
			return LastWayInErr
		}
		return nil
	})
	if err != nil {
		return err
	}
	slog.InfoContext(ctx, "removed passkey", "user", ctx.User.ID)
	return nil
}
