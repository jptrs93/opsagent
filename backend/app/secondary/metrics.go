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
		out.Send(&apigen.MsgToPrimary{LogQueryError: "metrics store is not running", LogRequestID: req.RequestID})
		return
	}
	resp, err := store.QueryResponse(ctx, req)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		slog.WarnContext(ctx, "metrics query failed", "dep", req.DeploymentID, "err", err)
		out.Send(&apigen.MsgToPrimary{LogQueryError: err.Error(), LogRequestID: req.RequestID})
		return
	}
	out.Send(&apigen.MsgToPrimary{MetricsQueryResponse: resp, LogRequestID: req.RequestID})
}

func runMetricsLatest(ctx context.Context, out *outbox, req *apigen.MetricsLatestRequest) {
	store := metricstore.Default
	if store == nil {
		out.Send(&apigen.MsgToPrimary{LogQueryError: "metrics store is not running", LogRequestID: req.RequestID})
		return
	}
	out.Send(&apigen.MsgToPrimary{MetricsLatestResponse: store.LatestResponse(), LogRequestID: req.RequestID})
}
