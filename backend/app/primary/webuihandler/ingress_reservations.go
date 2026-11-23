package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/lib/ingressplan"
)

// platformReservations resolves the platform's own listeners from
// the current cluster settings.
func (h *Handler) platformReservations() []ingressplan.Reservation {
	settings := h.SystemConfig.Snapshot().Settings
	return deployments.ReservationsFromSettings(h.NodeID, &settings, func(v apigen.StringSetting) string {
		return h.SystemConfig.MustLoadStringSetting(v)
	}, func(v apigen.BoolSetting) bool {
		return h.SystemConfig.MustLoadBoolSetting(v)
	})
}
