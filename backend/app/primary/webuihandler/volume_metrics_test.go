package webuihandler

import (
	"context"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/lib/metrics"
	"github.com/jptrs93/opsagent/backend/lib/metrics/metricstore"
)

func TestMetricsLatestListsTheVolumesTheViewerCanSee(t *testing.T) {
	h, staging := newEnforcementTestHandler(t)
	node := ensureTestNode(h.Store, "primary", "primary-id")
	h.NodeID = node.ID
	spec := remoteDeploymentSpec("nginx", hostNetworking())
	web := createTestDeployment(h.Store, "primary", nodes.DefaultSpaceID, "web", &spec)
	db := createTestDeployment(h.Store, "primary", staging.ID, "db", &spec)

	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	h.Metrics = metricstore.Start(ctx, t.TempDir(), 99)
	at := time.Now().Add(-time.Minute)
	h.Metrics.Volumes = func() []metrics.VolumeReading {
		return []metrics.VolumeReading{
			{DeploymentID: int32(db.Deployment.ID), Time: at, Usage: metrics.VolumeUsage{Bytes: 2 << 30, Inodes: 40}},
			{DeploymentID: int32(web.Deployment.ID), Time: at, Usage: metrics.VolumeUsage{Bytes: 8192, Inodes: 2}},
			{DeploymentID: 4040, Time: at, Usage: metrics.VolumeUsage{Bytes: 1, Inodes: 1}},
		}
	}

	all, err := h.PostV1MetricsLatest(enforceCtx(1, false), &apigen.MetricsLatestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(all.Volumes) != 2 {
		t.Fatalf("cluster admin got %d volumes, want the two of live deployments: %+v", len(all.Volumes), all.Volumes)
	}
	for _, v := range all.Volumes {
		if v.NodeID != h.NodeID {
			t.Fatalf("volume carries node %d, want the node that was asked, %d", v.NodeID, h.NodeID)
		}
	}
	if all.Volumes[0].DeploymentID > all.Volumes[1].DeploymentID {
		t.Fatalf("volumes are not in deployment order: %+v", all.Volumes)
	}

	own, err := h.PostV1MetricsLatest(enforceCtx(2, false), &apigen.MetricsLatestRequest{})
	if err != nil {
		t.Fatal(err)
	}
	if len(own.Volumes) != 1 || own.Volumes[0].DeploymentID != web.Deployment.ID || own.Volumes[0].Bytes != 8192 {
		t.Fatalf("space operator got %+v, want only the volume of its own space", own.Volumes)
	}
}
