package webuihandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/lib/ingressplan"
)

// webUIReservations resolves the platform's own listeners on the primary from
// the current cluster settings. Handlers built without a config service (unit
// tests) reserve nothing.
func (h *Handler) webUIReservations() []ingressplan.Reservation {
	if h.Config == nil || h.SystemConfig == nil {
		return nil
	}
	return deployments.ReservationsFromSettings(h.NodeID, h.Config, func(v apigen.StringSetting) string {
		return h.SystemConfig.MustLoadStringSetting(v)
	}, func(v apigen.BoolSetting) bool {
		return h.SystemConfig.MustLoadBoolSetting(v)
	})
}
