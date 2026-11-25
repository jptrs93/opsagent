package webuihandler

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/metrics"
	"github.com/jptrs93/opsagent/backend/lib/metrics/metricstore"
)

func newNodeMetricsHandler(t *testing.T) (*Handler, *apigen.Space, time.Time) {
	t.Helper()
	h, staging := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "primary", "primary-id")
	h.NodeID = node.ID
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.NodeMetrics = metricstore.StartNode(ctx, t.TempDir(), int32(node.ID))
	now := time.Now().Truncate(10 * time.Second)
	for i, cpu := range []uint64{1_000_000, 6_000_000} {
		h.NodeMetrics.ConsumeNode(ctx, metrics.NodeSample{
			Time:   now.Add(time.Duration(i-1) * 10 * time.Second),
			CPU:    &metrics.HostCPU{UsageUsec: cpu, Count: 4},
			DataFS: &metrics.FSUsage{TotalBytes: 100, FreeBytes: 40, AvailBytes: 30},
		})
	}
	return h, staging, now
}

func hideNodeFromDefaultSpace(t *testing.T, h *Handler, staging *apigen.Space) {
	t.Helper()
	if _, err := h.PostV1NodeAccessPoliciesCreate(enforceCtx(1, false), &apigen.NodeAccessPolicyCreateRequest{
		Name: "staging only", Nodes: exactNodes(h.NodeID), Rule: nodeAccessRule(true, allSpacesBut(staging.ID)),
	}); err != nil {
		t.Fatalf("narrow the node to staging: %v", err)
	}
}

func TestNodeMetricsLatestCarriesThisNodeForViewersWhoSeeIt(t *testing.T) {
	h, staging, _ := newNodeMetricsHandler(t)
	admin, spaceop := enforceCtx(1, false), enforceCtx(2, false)

	for name, ctx := range map[string]apigen.Context{"cluster admin": admin, "space operator": spaceop} {
		resp, err := h.PostV1MetricsNodesLatest(ctx, &apigen.NodeMetricsLatestRequest{})
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if len(resp.Entries) != 1 {
			t.Fatalf("%s: got %d entries, want this node's", name, len(resp.Entries))
		}
		e := resp.Entries[0]
		if uint64(e.Sample.NodeID) != h.NodeID || e.Sample.FsDataTotalBytes.Value != 100 || e.Sample.FsDataFreeBytes.Value != 40 {
			t.Fatalf("%s: entry = %+v, want node %d with its data filesystem", name, e.Sample, h.NodeID)
		}
		rate := 0.0
		for _, r := range e.Rates {
			if r.Field == "cpu_usage_usec" {
				rate = r.PerSecond
			}
		}
		if rate != 500_000 {
			t.Fatalf("%s: cpu rate = %v, want 500000", name, rate)
		}
	}

	hideNodeFromDefaultSpace(t, h, staging)
	resp, err := h.PostV1MetricsNodesLatest(spaceop, &apigen.NodeMetricsLatestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Entries) != 0 {
		t.Fatalf("a node the operator cannot see reported metrics: %+v", resp.Entries)
	}
	if resp, err := h.PostV1MetricsNodesLatest(admin, &apigen.NodeMetricsLatestRequest{}); err != nil || len(resp.Entries) != 1 {
		t.Fatalf("cluster admin lost the node: %+v, %v", resp, err)
	}
}

func TestNodeMetricsQueryRollsUpThisNode(t *testing.T) {
	h, _, now := newNodeMetricsHandler(t)
	resp, err := h.PostV1MetricsNodesQuery(enforceCtx(2, false), &apigen.NodeMetricsQueryRequest{
		NodeID:    h.NodeID,
		TimeStart: apigen.Some(now.Add(-10 * time.Minute)),
		TimeEnd:   apigen.Some(now.Add(time.Minute)),
		Fields:    []string{"fs_data_used_bytes", "cpu_usage_usec"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.StepMs != 10_000 || resp.Buckets == 0 {
		t.Fatalf("plan = step %d ms over %d buckets, want the 10 s step chosen for an 11 minute range", resp.StepMs, resp.Buckets)
	}
	seen := map[string]bool{}
	for _, ser := range resp.Series {
		seen[ser.Field] = true
		if ser.NodeID != h.NodeID || len(ser.Values) != int(resp.Buckets) {
			t.Fatalf("series = %+v, want node %d over %d buckets", ser, h.NodeID, resp.Buckets)
		}
	}
	if !seen["fs_data_used_bytes"] || !seen["cpu_usage_usec"] || len(seen) != 2 {
		t.Fatalf("series fields = %v, want the two requested", seen)
	}
}

func TestNodeMetricsQueryRefusesWhatItCannotAnswer(t *testing.T) {
	h, staging, now := newNodeMetricsHandler(t)
	admin, spaceop := enforceCtx(1, false), enforceCtx(2, false)
	other := ensureTestNode(h.Store, "secondary", "secondary-id")
	valid := func(nodeID uint64) *apigen.NodeMetricsQueryRequest {
		return &apigen.NodeMetricsQueryRequest{NodeID: nodeID, TimeStart: apigen.Some(now.Add(-time.Hour)), TimeEnd: apigen.Some(now)}
	}

	if _, err := h.PostV1MetricsNodesQuery(admin, &apigen.NodeMetricsQueryRequest{}); !errors.Is(err, MissingNodeIDErr) {
		t.Fatalf("no node id: got %v, want MissingNodeIDErr", err)
	}
	if _, err := h.PostV1MetricsNodesQuery(admin, valid(987654)); !errors.Is(err, NodeNotFoundErr) {
		t.Fatalf("unknown node: got %v, want NodeNotFoundErr", err)
	}
	unknownField := valid(h.NodeID)
	unknownField.Fields = []string{"mem_current"}
	if _, err := h.PostV1MetricsNodesQuery(admin, unknownField); err == nil {
		t.Fatal("a container metric was accepted as a node metric")
	}
	backwards := valid(h.NodeID)
	backwards.TimeStart, backwards.TimeEnd = backwards.TimeEnd, backwards.TimeStart
	if _, err := h.PostV1MetricsNodesQuery(admin, backwards); err == nil {
		t.Fatal("a range that ends before it starts was accepted")
	}

	var apiErr apigen.ApiErr
	if _, err := h.PostV1MetricsNodesQuery(admin, valid(other.ID)); !errors.As(err, &apiErr) || apiErr.Code != http.StatusBadGateway || apiErr.InternalErr != "secondary_not_connected" {
		t.Fatalf("another node with no cluster session: got %v, want secondary_not_connected and never this node's own data", err)
	}

	hideNodeFromDefaultSpace(t, h, staging)
	if _, err := h.PostV1MetricsNodesQuery(spaceop, valid(h.NodeID)); !errors.Is(err, NodeNotFoundErr) {
		t.Fatalf("hidden node: got %v, want NodeNotFoundErr", err)
	}
	if _, err := h.PostV1MetricsNodesQuery(admin, valid(h.NodeID)); err != nil {
		t.Fatalf("cluster admin on the hidden node: %v", err)
	}
}
