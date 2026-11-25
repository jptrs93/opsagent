package webuihandler

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"net/http"
	"slices"
	"sync"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/clusterhandler"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/metrics/metricstore"
)

const metricsLatestTimeout = 5 * time.Second

func (h *Handler) metricsNodes(cfg *apigen.DeploymentRecord) []uint64 {
	nodes := []uint64{cfg.Deployment.PlacementNodeID()}
	for _, st := range h.Store.FetchScheduledSnapshot(nil) {
		if st.Instance.Deployment.DeploymentID == cfg.Deployment.ID && st.Instance.NodeID > 0 && !slices.Contains(nodes, st.Instance.NodeID) {
			nodes = append(nodes, st.Instance.NodeID)
		}
	}
	return nodes
}

func (h *Handler) PostV1MetricsQuery(ctx apigen.Context, req *apigen.MetricsQueryRequest) (*apigen.MetricsQueryResponse, error) {
	if req.DeploymentID == 0 {
		return nil, MissingKeyErr
	}
	if req.Run < 0 || req.StepMs < 0 {
		return nil, deployments.InvalidConfigErrf("scope values must not be negative")
	}
	cfg := h.findConfigByID(req.DeploymentID)
	if cfg == nil {
		return nil, deployments.NotFoundErr
	}
	if err := h.requireEntityAccess(ctx, vViewLogs, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID, deployments.NotFoundErr); err != nil {
		return nil, err
	}
	if _, _, err := metricstore.ResolveRange(req.TimeStart, req.TimeEnd, time.Now()); err != nil {
		return nil, deployments.InvalidConfigErrf("%s", err.Error())
	}
	if _, err := metricstore.RequestFields(req.Fields); err != nil {
		return nil, deployments.InvalidConfigErrf("%s", err.Error())
	}
	if req.StepMs < metricstore.MinStep.Milliseconds() {
		from, to, _ := metricstore.ResolveRange(req.TimeStart, req.TimeEnd, time.Now())
		req.StepMs = metricstore.ChooseStep(from, to, metricstore.DefaultMaxPoints).Milliseconds()
	}
	nodes := h.metricsNodes(cfg)
	if req.TargetNodeID > 0 {
		nodes = []uint64{req.TargetNodeID}
	}
	type reply struct {
		node uint64
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

func (h *Handler) metricsQueryOnNode(ctx context.Context, nodeID uint64, req *apigen.MetricsQueryRequest) (*apigen.MetricsQueryResponse, error) {
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
		local := h.Metrics.LatestResponse()
		out.Entries = local.Entries
		out.Volumes = stampVolumes(local.Volumes, h.NodeID)
	}
	if h.Cluster != nil {
		type reply struct {
			node uint64
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
			out.Volumes = append(out.Volumes, stampVolumes(r.resp.Volumes, r.node)...)
		}
	}
	visible := make(map[uint64]bool)
	canSee := func(id uint64) bool {
		ok, seen := visible[id]
		if !seen {
			cfg := h.findConfigByID(id)
			ok = cfg != nil && h.canAccess(ctx, vViewLogs, eDeployment, cfg.Deployment.SpaceID, cfg.Deployment.ID)
			visible[id] = ok
		}
		return ok
	}
	out.Entries = slices.DeleteFunc(out.Entries, func(e apigen.MetricsLatestEntry) bool {
		return e.Sample.DeploymentID <= 0 || !canSee(uint64(e.Sample.DeploymentID))
	})
	out.Volumes = slices.DeleteFunc(out.Volumes, func(v apigen.MetricsVolumeUsage) bool {
		return v.DeploymentID == 0 || !canSee(v.DeploymentID)
	})
	slices.SortFunc(out.Entries, func(a, b apigen.MetricsLatestEntry) int {
		return metricstore.CompareKey(metricstore.Key(&a.Sample), metricstore.Key(&b.Sample))
	})
	slices.SortFunc(out.Volumes, func(a, b apigen.MetricsVolumeUsage) int {
		if c := cmp.Compare(a.DeploymentID, b.DeploymentID); c != 0 {
			return c
		}
		return cmp.Compare(a.NodeID, b.NodeID)
	})
	return out, nil
}

func stampVolumes(volumes []apigen.MetricsVolumeUsage, nodeID uint64) []apigen.MetricsVolumeUsage {
	for i := range volumes {
		volumes[i].NodeID = nodeID
	}
	return volumes
}

func secondaryMetricsErr(nodeID uint64, err error) error {
	var notConnected *clusterhandler.NodeNotConnectedError
	if errors.As(err, &notConnected) {
		return apigen.NewApiErr(fmt.Sprintf("Secondary node %d is not connected", nodeID), "secondary_not_connected", http.StatusBadGateway)
	}
	return apigen.NewApiErr(fmt.Sprintf("Metrics query on secondary node %d failed: %v", nodeID, err), "secondary_metrics_query_failed", http.StatusBadGateway)
}

const nodeMetricsQueryTimeout = 20 * time.Second

var MissingNodeIDErr = apigen.NewApiErr("Node id is required", "missing_node_id", http.StatusBadRequest)

func (h *Handler) PostV1MetricsNodesQuery(ctx apigen.Context, req *apigen.NodeMetricsQueryRequest) (*apigen.NodeMetricsQueryResponse, error) {
	if req.NodeID == 0 {
		return nil, MissingNodeIDErr
	}
	if req.StepMs < 0 {
		return nil, deployments.InvalidConfigErrf("step_ms must not be negative")
	}
	if !h.memberNodeExists(req.NodeID) || !h.nodeVisible(ctx, req.NodeID) {
		return nil, NodeNotFoundErr
	}
	from, to, err := metricstore.ResolveRange(req.TimeStart, req.TimeEnd, time.Now())
	if err != nil {
		return nil, deployments.InvalidConfigErrf("%s", err.Error())
	}
	if _, err := metricstore.NodeRequestFields(req.Fields); err != nil {
		return nil, deployments.InvalidConfigErrf("%s", err.Error())
	}
	if req.StepMs < metricstore.MinStep.Milliseconds() {
		req.StepMs = metricstore.ChooseStep(from, to, metricstore.DefaultMaxPoints).Milliseconds()
	}
	resp, err := h.nodeMetricsQueryOnNode(ctx, req)
	if err != nil {
		return nil, err
	}
	for i := range resp.Series {
		resp.Series[i].NodeID = req.NodeID
	}
	return resp, nil
}

func (h *Handler) memberNodeExists(nodeID uint64) bool {
	for _, node := range nodes.ListNodes(h.Store.Queries()) {
		if node != nil && node.ID == nodeID {
			return true
		}
	}
	return false
}

func (h *Handler) nodeMetricsQueryOnNode(ctx context.Context, req *apigen.NodeMetricsQueryRequest) (*apigen.NodeMetricsQueryResponse, error) {
	if req.NodeID != h.NodeID {
		if h.Cluster == nil {
			return nil, secondaryMetricsErr(req.NodeID, &clusterhandler.NodeNotConnectedError{NodeID: req.NodeID})
		}
		queryCtx, cancel := context.WithTimeout(ctx, nodeMetricsQueryTimeout)
		defer cancel()
		r := *req
		resp, err := h.Cluster.RequestNodeMetricsQuery(queryCtx, req.NodeID, &r)
		if err != nil {
			return nil, secondaryMetricsErr(req.NodeID, err)
		}
		return resp, nil
	}
	if h.NodeMetrics == nil {
		return nil, apigen.NewApiErr("Node metrics store is not running", "metrics_unavailable", http.StatusInternalServerError)
	}
	return h.NodeMetrics.QueryResponse(ctx, req)
}

func (h *Handler) PostV1MetricsNodesLatest(ctx apigen.Context, _ *apigen.NodeMetricsLatestRequest) (*apigen.NodeMetricsLatestResponse, error) {
	out := &apigen.NodeMetricsLatestResponse{}
	access := h.readNodeAccess()
	if h.NodeMetrics != nil && h.nodeVisibleIn(ctx, access, h.NodeID) {
		out.Entries = stampNodeMetrics(h.NodeMetrics.LatestResponse().Entries, h.NodeID)
	}
	if h.Cluster != nil {
		type reply struct {
			node uint64
			resp *apigen.NodeMetricsLatestResponse
			err  error
		}
		fanCtx, cancel := context.WithTimeout(ctx, metricsLatestTimeout)
		defer cancel()
		replies := make(chan reply)
		pending := 0
		for node := range h.Cluster.ConnectedNodes() {
			if node == h.NodeID || !h.nodeVisibleIn(ctx, access, node) {
				continue
			}
			pending++
			go func() {
				resp, err := h.Cluster.RequestNodeMetricsLatest(fanCtx, node)
				replies <- reply{node: node, resp: resp, err: err}
			}()
		}
		for ; pending > 0; pending-- {
			r := <-replies
			if r.err != nil {
				out.Warnings = append(out.Warnings, fmt.Sprintf("node %d: %v", r.node, r.err))
				continue
			}
			out.Entries = append(out.Entries, stampNodeMetrics(r.resp.Entries, r.node)...)
		}
	}
	slices.SortFunc(out.Entries, func(a, b apigen.NodeMetricsLatestEntry) int {
		return cmp.Compare(a.Sample.NodeID, b.Sample.NodeID)
	})
	slices.Sort(out.Warnings)
	return out, nil
}

// stampNodeMetrics labels entries with the node they were requested from: a
// node's own claim about which node it is must not decide whose row they fill.
func stampNodeMetrics(entries []apigen.NodeMetricsLatestEntry, nodeID uint64) []apigen.NodeMetricsLatestEntry {
	for i := range entries {
		entries[i].Sample.NodeID = int32(nodeID)
	}
	return entries
}
