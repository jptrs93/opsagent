package webuihandler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/lib/metrics/metricstore"
)

const metricsLatestTimeout = 5 * time.Second

func (h *Handler) metricsNodes(cfg *apigen.Deployment) []int32 {
	nodes := []int32{cfg.Def.NodeID}
	for _, st := range h.Store.FetchScheduledSnapshot(nil) {
		if st.Instance.DeploymentID == cfg.ID && st.Instance.NodeID > 0 && !slices.Contains(nodes, st.Instance.NodeID) {
			nodes = append(nodes, st.Instance.NodeID)
		}
	}
	return nodes
}

func (h *Handler) PostV1MetricsQuery(ctx apigen.Context, req *apigen.MetricsQueryRequest) (*apigen.MetricsQueryResponse, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}
	if req.SpecVersion < 0 || req.Run < 0 || req.ScheduledInstanceID < 0 || req.StepMs < 0 {
		return nil, invalidConfigErrf("scope values must not be negative")
	}
	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil {
		return nil, DeploymentNotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, int64(cfg.Def.SpaceID), int64(cfg.ID), DeploymentNotFoundErr); err != nil {
		return nil, err
	}
	if _, _, err := metricstore.ResolveRange(req.TimeStart, req.TimeEnd, time.Now()); err != nil {
		return nil, invalidConfigErrf("%s", err.Error())
	}
	if _, err := metricstore.RequestFields(req.Fields); err != nil {
		return nil, invalidConfigErrf("%s", err.Error())
	}
	if req.StepMs < metricstore.MinStep.Milliseconds() {
		from, to, _ := metricstore.ResolveRange(req.TimeStart, req.TimeEnd, time.Now())
		req.StepMs = metricstore.ChooseStep(from, to, metricstore.DefaultMaxPoints).Milliseconds()
	}
	nodes := h.metricsNodes(cfg)
	if req.TargetNodeID > 0 {
		nodes = []int32{req.TargetNodeID}
	}
	type reply struct {
		node int32
		resp *apigen.MetricsQueryResponse
		err  error
	}
	replies := make([]reply, len(nodes))
	var wg sync.WaitGroup
	for i, node := range nodes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r := reply{node: node}
			r.resp, r.err = h.metricsQueryOnNode(ctx, node, req)
			replies[i] = r
		}()
	}
	wg.Wait()
	var out *apigen.MetricsQueryResponse
	var warnings []string
	for _, r := range replies {
		if r.err != nil {
			if len(nodes) == 1 {
				return nil, r.err
			}
			warnings = append(warnings, fmt.Sprintf("node %d: %v", r.node, r.err))
			continue
		}
		if out == nil {
			out = r.resp
			continue
		}
		out.Series = append(out.Series, r.resp.Series...)
		out.ScannedRows += r.resp.ScannedRows
		out.TookMs = max(out.TookMs, r.resp.TookMs)
		out.Warnings = append(out.Warnings, r.resp.Warnings...)
	}
	if out == nil {
		return nil, apigen.NewApiErr("Metrics query failed on every node", "metrics_query_failed", http.StatusBadGateway)
	}
	out.Warnings = append(out.Warnings, warnings...)
	return out, nil
}

func (h *Handler) metricsQueryOnNode(ctx context.Context, nodeID int32, req *apigen.MetricsQueryRequest) (*apigen.MetricsQueryResponse, error) {
	if nodeID > 0 && nodeID != h.NodeID && h.Cluster != nil {
		r := *req
		resp, err := h.Cluster.RequestMetricsQuery(ctx, nodeID, &r)
		if err != nil {
			return nil, secondaryMetricsErr(nodeID, err)
		}
		return resp, nil
	}
	if h.Metrics == nil {
		return nil, apigen.NewApiErr("Metrics store is not running", "metrics_unavailable", http.StatusInternalServerError)
	}
	return h.Metrics.QueryResponse(ctx, req)
}

func (h *Handler) PostV1MetricsLatest(ctx apigen.Context, _ *apigen.MetricsLatestRequest) (*apigen.MetricsLatestResponse, error) {
	out := &apigen.MetricsLatestResponse{}
	if h.Metrics != nil {
		out.Entries = h.Metrics.LatestResponse().Entries
	}
	if h.Cluster != nil {
		type reply struct {
			node int32
			resp *apigen.MetricsLatestResponse
			err  error
		}
		fanCtx, cancel := context.WithTimeout(ctx, metricsLatestTimeout)
		defer cancel()
		replies := make(chan reply)
		pending := 0
		for node := range h.Cluster.ConnectedNodes() {
			if node == h.NodeID {
				continue
			}
			pending++
			go func() {
				resp, err := h.Cluster.RequestMetricsLatest(fanCtx, node)
				replies <- reply{node: node, resp: resp, err: err}
			}()
		}
		for ; pending > 0; pending-- {
			r := <-replies
			if r.err != nil {
				out.Warnings = append(out.Warnings, fmt.Sprintf("node %d: %v", r.node, r.err))
				continue
			}
			out.Entries = append(out.Entries, r.resp.Entries...)
		}
	}
	visible := make(map[int32]bool)
	out.Entries = slices.DeleteFunc(out.Entries, func(e *apigen.MetricsLatestEntry) bool {
		if e.Sample == nil {
			return true
		}
		id := e.Sample.DeploymentID
		ok, seen := visible[id]
		if !seen {
			cfg := h.findConfigByID(id)
			ok = cfg != nil && h.canAccess(ctx, vViewLogs, eDeployment, int64(cfg.Def.SpaceID), int64(cfg.ID))
			visible[id] = ok
		}
		return !ok
	})
	slices.SortFunc(out.Entries, func(a, b *apigen.MetricsLatestEntry) int {
		return metricstore.CompareKey(metricstore.Key(a.Sample), metricstore.Key(b.Sample))
	})
	return out, nil
}

func secondaryMetricsErr(nodeID int32, err error) error {
	var notConnected *clusterhandler.NodeNotConnectedError
	if errors.As(err, &notConnected) {
		return apigen.NewApiErr(fmt.Sprintf("Secondary node %d is not connected", nodeID), "secondary_not_connected", http.StatusBadGateway)
	}
	return apigen.NewApiErr(fmt.Sprintf("Metrics query on secondary node %d failed: %v", nodeID, err), "secondary_metrics_query_failed", http.StatusBadGateway)
}
