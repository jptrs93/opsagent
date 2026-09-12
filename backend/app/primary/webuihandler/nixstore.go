package webuihandler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func (h *Handler) PostV1NixStoreReset(ctx apigen.Context, req *apigen.NixStoreResetRequest) error {
	if err := h.requireAccess(ctx, vUpdate, eCluster, 0, 0); err != nil {
		return err
	}
	if h.NixStores == nil {
		return apigen.NewApiErr("nix store resets are not available", "nix_store_unavailable", http.StatusServiceUnavailable)
	}
	if err := h.NixStores.RequestReset(ctx, req.Repo, time.Now()); err != nil {
		return apigen.NewApiErr(err.Error(), "nix_store_reset_invalid", http.StatusBadRequest)
	}
	slog.InfoContext(ctx, "nix store reset requested", "repo", req.Repo)
	return nil
}
