package webuihandler

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb"
)

// deleteDeployment removes a deployment through the handler so the tombstone is
// written exactly as it is in production, tombstone version and all.
func deleteDeployment(t *testing.T, h *Handler, cfg *apigen.DeploymentConfig) {
	t.Helper()
	current := h.findConfigByID(cfg.ID)
	if current == nil {
		t.Fatalf("deployment %d not found", cfg.ID)
	}
	err := h.PostV1DeploymentsDelete(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentDeleteRequest{
		DeploymentID: current.ID,
		Version:      current.Version + 1,
	})
	if err != nil {
		t.Fatalf("delete %d: %v", cfg.ID, err)
	}
}

func createStoppedDeployment(t *testing.T, h *Handler, nodeID int32, name string) *apigen.DeploymentConfig {
	t.Helper()
	cfg, err := h.PostV1DeploymentsCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(nodeID, name, false))
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return cfg
}

func recentlyDeleted(t *testing.T, h *Handler, limit int32) []*apigen.DeploymentConfig {
	t.Helper()
	res, err := h.PostV1DeploymentsRecentlyDeleted(apigen.Context{Ctx: context.Background()},
		&apigen.RecentlyDeletedDeploymentsRequest{Limit: limit})
	if err != nil {
		t.Fatalf("recently deleted: %v", err)
	}
	return res.Items
}

func deletedNames(items []*apigen.DeploymentConfig) []string {
	out := make([]string, 0, len(items))
	for _, cfg := range items {
		out = append(out, cfg.Identity.Name)
	}
	return out
}

func newRecentlyDeletedHandler(t *testing.T) (*Handler, int32) {
	t.Helper()
	store := primarydb.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := store.EnsurePrimaryNode("primary", "primary")
	return &Handler{Store: store, GitVersions: &fakeGitSourceProvider{}}, node.ID
}

func TestRecentlyDeletedListsOnlyDeletedNewestFirst(t *testing.T) {
	h, nodeID := newRecentlyDeletedHandler(t)
	first := createStoppedDeployment(t, h, nodeID, "first")
	second := createStoppedDeployment(t, h, nodeID, "second")
	createStoppedDeployment(t, h, nodeID, "live")

	deleteDeployment(t, h, first)
	deleteDeployment(t, h, second)

	got := deletedNames(recentlyDeleted(t, h, 0))
	// Newest deletion first, and a deployment that was never deleted is absent.
	want := []string{"second", "first"}
	if len(got) != len(want) {
		t.Fatalf("deleted = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("deleted = %v, want %v", got, want)
		}
	}
}

func TestRecentlyDeletedRetainsForkableSpec(t *testing.T) {
	h, nodeID := newRecentlyDeletedHandler(t)
	created := createStoppedDeployment(t, h, nodeID, "web")
	deleteDeployment(t, h, created)

	items := recentlyDeleted(t, h, 0)
	if len(items) != 1 {
		t.Fatalf("deleted count = %d, want 1", len(items))
	}
	cfg := items[0]
	// The spec is what a fork is seeded from, so the tombstone must carry it
	// intact along with the identity the deployment ran under.
	if cfg.Spec.Container1Spec == nil {
		t.Fatal("tombstone lost the container spec")
	}
	if got := cfg.Spec.Container1Spec.Source.NixDockerBuild.Repo; got != "github.com/acme/app" {
		t.Fatalf("repo = %q, want github.com/acme/app", got)
	}
	if cfg.Identity.Name != "web" || cfg.NodeID != nodeID {
		t.Fatalf("identity = %q/%d, want web/%d", cfg.Identity.Name, cfg.NodeID, nodeID)
	}
	if !cfg.Deleted {
		t.Fatal("tombstone is not marked deleted")
	}
}

func TestRecentlyDeletedAppliesLimit(t *testing.T) {
	h, nodeID := newRecentlyDeletedHandler(t)
	for _, name := range []string{"a", "b", "c"} {
		deleteDeployment(t, h, createStoppedDeployment(t, h, nodeID, name))
	}

	got := deletedNames(recentlyDeleted(t, h, 2))
	if len(got) != 2 || got[0] != "c" || got[1] != "b" {
		t.Fatalf("limited = %v, want [c b]", got)
	}
}

func TestRecentlyDeletedBoundsUnusableLimits(t *testing.T) {
	h, nodeID := newRecentlyDeletedHandler(t)
	// More tombstones than the default limit, so a limit that is ignored or
	// honoured verbatim returns a different count from one that is clamped.
	total := recentlyDeletedDefaultLimit + 3
	for i := 0; i < total; i++ {
		deleteDeployment(t, h, createStoppedDeployment(t, h, nodeID, fmt.Sprintf("app-%02d", i)))
	}

	// Deleted configs are never pruned, so an absent or oversized limit must fall
	// back to the default rather than dumping every tombstone the install ever had.
	for _, limit := range []int32{0, -1, recentlyDeletedMaxLimit + 1} {
		if got := len(recentlyDeleted(t, h, limit)); got != recentlyDeletedDefaultLimit {
			t.Fatalf("limit %d returned %d, want %d", limit, got, recentlyDeletedDefaultLimit)
		}
	}
	// A limit inside the cap is honoured exactly.
	if got := len(recentlyDeleted(t, h, int32(total))); got != total {
		t.Fatalf("limit %d returned %d, want %d", total, got, total)
	}
}

func TestRecentlyDeletedOmitsInternalDeployments(t *testing.T) {
	h, nodeID := newRecentlyDeletedHandler(t)
	regular := createStoppedDeployment(t, h, nodeID, "web")
	deleteDeployment(t, h, regular)

	// Internal opendeploy deployments are recreated by the primary, not through
	// the create API, so a tombstone for one is not forkable. Delete it through
	// the store directly: the handler refuses internal deletes outright.
	h.Store.EnsureSystemDeployment(nodeID, "v1.0.0")
	var internal *apigen.DeploymentConfig
	for _, cfg := range h.Store.FetchDeploymentSnapshot(nil) {
		if internaldeploy.IsInternalConfig(&cfg) {
			internal = &cfg
			break
		}
	}
	if internal == nil {
		t.Fatal("no internal deployment was created")
	}
	deleted := true
	_, _, ok := h.Store.UpdateDeploymentConfig(apigen.Context{Ctx: context.Background()}, internal.ID, primarydb.DeploymentConfigUpdate{
		ExpectedVersion: internal.Version + 1,
		Spec:            &internal.Spec,
		Deleted:         &deleted,
	})
	if !ok {
		t.Fatal("deleting the internal deployment failed")
	}

	items := recentlyDeleted(t, h, 0)
	if len(items) != 1 || items[0].Identity.Name != "web" {
		t.Fatalf("deleted = %v, want [web]", deletedNames(items))
	}
	for _, cfg := range items {
		if internaldeploy.IsInternalConfig(cfg) {
			t.Fatalf("internal deployment %q listed", cfg.Identity.Name)
		}
	}
}
