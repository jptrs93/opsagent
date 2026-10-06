package webuihandler

import (
	"context"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

// deleteDeployment removes a deployment through the handler so the tombstone is
// written exactly as it is in production, tombstone version and all.
func deleteDeployment(t *testing.T, h *Handler, cfg *apigen.DeploymentRecord) {
	t.Helper()
	current := h.findConfigByID(cfg.Deployment.ID)
	if current == nil {
		t.Fatalf("deployment %d not found", cfg.Deployment.ID)
	}
	err := h.PostV1DeploymentsDelete(apigen.Context{Ctx: context.Background()}, &apigen.DeploymentDeleteRequest{
		DeploymentID: current.Deployment.ID,
		ExpectedSeq:  current.Meta.UpdatedSeq,
	})
	if err != nil {
		t.Fatalf("delete %d: %v", cfg.Deployment.ID, err)
	}
}

func createStoppedDeployment(t *testing.T, h *Handler, nodeID uint64, name string) *apigen.DeploymentRecord {
	t.Helper()
	cfg, err := h.deploymentsCreate(apigen.Context{Ctx: context.Background()}, nixCreateRequest(nodeID, name, false))
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return cfg
}

func recentlyDeleted(t *testing.T, h *Handler, limit int32) []apigen.DeploymentRecord {
	t.Helper()
	res, err := h.PostV1DeploymentsRecentlyDeleted(apigen.Context{Ctx: context.Background()},
		&apigen.RecentlyDeletedDeploymentsRequest{Limit: limit})
	if err != nil {
		t.Fatalf("recently deleted: %v", err)
	}
	return res.Items
}

func deletedNames(items []apigen.DeploymentRecord) []string {
	out := make([]string, 0, len(items))
	for _, item := range items {
		out = append(out, item.Deployment.Name)
	}
	return out
}

func newRecentlyDeletedHandler(t *testing.T) (*Handler, uint64) {
	t.Helper()
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := ensureTestNode(store, "primary", "primary")
	return &Handler{SystemConfig: &systemconfig.Service{}, Store: store, Queries: store.Queries(), GitVersions: &fakeGitSourceProvider{}}, node.ID
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
	cfg := items[0].Deployment
	// The spec is what a fork is seeded from, so the tombstone must carry it
	// intact along with the identity the deployment ran under.
	if cfg.Spec.Workload.Value.Container == nil {
		t.Fatal("tombstone lost the container spec")
	}
	if got := cfg.Spec.Workload.Value.Container.Source.Value.NixImageBuild.Repo; got != "github.com/acme/app" {
		t.Fatalf("repo = %q, want github.com/acme/app", got)
	}
	if cfg.Name != "web" || cfg.PlacementNodeID() != nodeID || cfg.ID != created.Deployment.ID {
		t.Fatalf("identity = %q/%d/%d, want web/%d/%d", cfg.Name, cfg.PlacementNodeID(), cfg.ID, nodeID, created.Deployment.ID)
	}
	if meta := items[0].Meta; !meta.Deleted || meta.Version != created.Meta.Version || meta.SpecVersion != created.Meta.SpecVersion || meta.UpdatedSeq <= created.Meta.UpdatedSeq {
		t.Fatalf("tombstone meta = %+v, want deleted at version %d after seq %d", items[0].Meta, created.Meta.Version, created.Meta.UpdatedSeq)
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
	const (
		recentlyDeletedDefaultLimit = 25
		recentlyDeletedMaxLimit     = 200
	)
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
	deployments.EnsureSystem(h.Store, nodeID, "v1.0.0")
	var internal *apigen.DeploymentRecord
	for _, cfg := range deployments.Active(h.Store.Queries(), nil) {
		if internaldeploy.IsInternalConfig(&cfg) {
			internal = &cfg
			break
		}
	}
	if internal == nil {
		t.Fatal("no internal deployment was created")
	}
	statetest.DeleteDeployment(h.Store, apigen.Context{Ctx: context.Background()}, internal.Deployment.ID)

	items := recentlyDeleted(t, h, 0)
	if len(items) != 1 || items[0].Deployment.Name != "web" {
		t.Fatalf("deleted = %v, want [web]", deletedNames(items))
	}
	for _, item := range items {
		if internaldeploy.IsInternalIdentity(item.Deployment.SpaceID, item.Deployment.Name) {
			t.Fatalf("internal deployment %q listed", item.Deployment.Name)
		}
	}
}
