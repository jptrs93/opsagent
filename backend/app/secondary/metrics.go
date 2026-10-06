package secondary

import (
	"context"
	"log/slog"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/metrics/metricstore"
)

func runMetricsQuery(ctx context.Context, out *outbox, req *apigen.MetricsQueryRequest) {
	ctx = logu.AddTag(ctx, "Metrics")
	store := metricstore.Default
	if store == nil {
		out.Send(&apigen.MsgToPrimary{LogQueryError: apigen.Some("metrics store is not running"), LogRequestID: apigen.Some(req.RequestID)})
		return
	}
	resp, err := store.QueryResponse(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.WarnContext(ctx, "metrics query failed", "dep", req.DeploymentID, "err", err)
		out.Send(&apigen.MsgToPrimary{LogQueryError: apigen.Some(err.Error()), LogRequestID: apigen.Some(req.RequestID)})
		return
	}
	out.Send(&apigen.MsgToPrimary{MetricsQueryResponse: apigen.Some(*resp), LogRequestID: apigen.Some(req.RequestID)})
}

func runMetricsLatest(ctx context.Context, out *outbox, req *apigen.MetricsLatestRequest) {
	store := metricstore.Default
	if store == nil {
		out.Send(&apigen.MsgToPrimary{LogQueryError: apigen.Some("metrics store is not running"), LogRequestID: apigen.Some(req.RequestID)})
		return
	}
	out.Send(&apigen.MsgToPrimary{MetricsLatestResponse: apigen.Some(*store.LatestResponse()), LogRequestID: apigen.Some(req.RequestID)})
}
