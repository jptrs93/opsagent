package state

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

// backfillScheduledInstanceDeploymentVersions is the one-time v0.0.554
// migration stamping deployment_version onto pre-split scheduled instance
// rows. The pinned version is the earliest event whose spec_version matches
// the instance's and whose def carries the instance's space — verified to
// exist for every production row before this shipped. Space lives in the def
// blob, so this cannot be a SQL migration. Rows are selected by
// deployment_version = 0, making re-runs no-ops; remove with the ADD COLUMN
// migration once every cluster has rolled forward.
func (s *Service) backfillScheduledInstanceDeploymentVersions() {
	ctx := logu.AddTag(context.Background(), "Store")
	rows := erru.Must(s.q.ListScheduledInstancesMissingDeploymentVersion(ctx))
	if len(rows) == 0 {
		return
	}
	eventsByDeployment := map[int64][]pq.DeploymentEvent{}
	for _, row := range rows {
		events, ok := eventsByDeployment[row.DeploymentID]
		if !ok {
			events = erru.Must(s.q.ListDeploymentEvents(ctx, row.DeploymentID))
			eventsByDeployment[row.DeploymentID] = events
		}
		var version int64
		for _, e := range events {
			if e.SpecVersion != row.DeploymentSpecVersion {
				continue
			}
			def := erru.Must(apigen.DecodeDeploymentDef(e.Value))
			if int64(def.SpaceID) == row.SpaceID {
				version = e.Version
				break
			}
		}
		if version == 0 {
			panic(fmt.Sprintf("deployment_version backfill: scheduled instance %d references deployment %d spec %d space %d, which matches no event row",
				row.ID, row.DeploymentID, row.DeploymentSpecVersion, row.SpaceID))
		}
		if err := s.q.SetScheduledInstanceDeploymentVersion(ctx, pq.SetScheduledInstanceDeploymentVersionParams{
			DeploymentVersion: version,
			ID:                row.ID,
		}); err != nil {
			panic(fmt.Sprintf("SetScheduledInstanceDeploymentVersion: %v", err))
		}
	}
	slog.InfoContext(ctx, fmt.Sprintf("stamped deployment_version on %d scheduled instances", len(rows)))
}
