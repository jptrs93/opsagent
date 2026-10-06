package deployments

import (
	"context"
	"net/netip"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

const netproxyFileDescriptorLimit = uint32(65_536)

func TestEnsureSystemDeploymentRepairsExistingSpec(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	created := statetest.MustCreateStoppedDeploymentForNode(store, apigen.Context{}, internaldeploy.SpaceID, internaldeploy.SelfName, node.ID, statetest.SpecWithVersion(""))
	statetest.SetDeploymentWorkloadState(store, apigen.Context{}, created.Deployment.ID, "v0.0.194", true)

	EnsureSystem(store, node.ID, "v0.0.195")

	var repaired *apigen.DeploymentRecord
	for _, cfg := range erru.Must(store.Queries().ListActiveDeployments(context.Background())) {
		if cfg.Deployment.ID == created.Deployment.ID {
			repaired = cfg
			break
		}
	}
	if repaired == nil {
		t.Fatal("repaired deployment not found")
	}
	if repaired.Meta.SpecVersion <= created.Meta.SpecVersion {
		t.Fatalf("version = %d, want repaired version above %d", repaired.Meta.SpecVersion, created.Meta.SpecVersion)
	}
	if !internaldeploy.IsSelfSpec(&repaired.Deployment.Spec) {
		t.Fatalf("spec was not repaired: %+v", repaired.Deployment.Spec)
	}
	if repaired.WorkloadVersion() != "v0.0.194" || !repaired.WorkloadRunning() {
		t.Fatalf("workload state = %q/%v, want preserved running v0.0.194", repaired.WorkloadVersion(), repaired.WorkloadRunning())
	}
}

func TestEnsureNetproxyDeploymentCreatesInternalConfig(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	cfg := EnsureNetproxy(store, node.ID, "v0.0.200")
	if cfg == nil {
		t.Fatal("netproxy config not returned")
	}
	if cfg.Deployment.PlacementNodeID() != node.ID || cfg.Deployment.SpaceID != internaldeploy.SpaceID || cfg.Deployment.Name != internaldeploy.NetproxyName {
		t.Fatalf("unexpected config identity: node=%d space=%d name=%q", cfg.Deployment.PlacementNodeID(), cfg.Deployment.SpaceID, cfg.Deployment.Name)
	}
	if !internaldeploy.IsNetproxyConfig(cfg) || !internaldeploy.IsInternalConfig(cfg) {
		t.Fatalf("netproxy config not recognized as internal: space=%d name=%q", cfg.Deployment.SpaceID, cfg.Deployment.Name)
	}
	if !cfg.WorkloadRunning() || cfg.WorkloadVersion() != "v0.0.200" {
		t.Fatalf("workload state = %q/%v, want running v0.0.200", cfg.WorkloadVersion(), cfg.WorkloadRunning())
	}
	if cfg.Deployment.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL || len(cfg.Deployment.Spec.Networking.PortForwarding) != 0 {
		t.Fatalf("unexpected networking config: %+v", cfg.Deployment.Spec.Networking)
	}
	if got := cfg.Deployment.Spec.Container().Runtime.FileDescriptorLimit; !got.Present || got.Value != netproxyFileDescriptorLimit {
		t.Fatalf("file descriptor limit = %v, want %d", got, netproxyFileDescriptorLimit)
	}
	again := EnsureNetproxy(store, node.ID, "v0.0.200")
	if again.Meta.SpecVersion != cfg.Meta.SpecVersion {
		t.Fatalf("ensure bumped unchanged netproxy version from %d to %d", cfg.Meta.SpecVersion, again.Meta.SpecVersion)
	}
}

func TestInternalDeploymentsAreScopedByNodeID(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	nodeA := nodes.EnsurePrimaryNode(store, "node-a", "node-a", netip.MustParseAddr("10.0.0.1"))
	nodeB := nodes.EnsurePrimaryNode(store, "node-b", "node-b", netip.MustParseAddr("10.0.0.2"))

	a := EnsureNetproxy(store, nodeA.ID, "v0.0.200")
	b := EnsureNetproxy(store, nodeB.ID, "v0.0.200")
	if a.Deployment.ID == b.Deployment.ID || a.Deployment.PlacementNodeID() != nodeA.ID || b.Deployment.PlacementNodeID() != nodeB.ID {
		t.Fatalf("netproxy deployments not scoped by node: a=%+v b=%+v", a, b)
	}
	if a.Deployment.SpaceID != b.Deployment.SpaceID || a.Deployment.Name != b.Deployment.Name {
		t.Fatalf("internal identities differ across nodes: a=%d/%q b=%d/%q", a.Deployment.SpaceID, a.Deployment.Name, b.Deployment.SpaceID, b.Deployment.Name)
	}
}

func TestEnsureNetproxyDeploymentRepairsExistingSpec(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	cfg := EnsureNetproxy(store, node.ID, "v0.0.200")
	broken := internaldeploy.NetproxySpec()
	broken.Container().Runtime.FileDescriptorLimit = apigen.Some[uint32](128)
	statetest.UpdateDeploymentSpecKeepingWorkload(store, apigen.Context{}, cfg.Deployment.ID, broken)
	brokenVersion := erru.Must(store.Queries().GetLatestDeployment(context.Background(), cfg.Deployment.ID)).Meta.SpecVersion

	repaired := EnsureNetproxy(store, node.ID, "v0.0.201")
	if repaired.Meta.SpecVersion <= brokenVersion {
		t.Fatalf("version = %d, want above broken version %d", repaired.Meta.SpecVersion, brokenVersion)
	}
	if got := repaired.Deployment.Spec.Container().Runtime.FileDescriptorLimit; !got.Present || got.Value != netproxyFileDescriptorLimit {
		t.Fatalf("file descriptor limit = %v, want %d", got, netproxyFileDescriptorLimit)
	}
	if repaired.WorkloadVersion() != "v0.0.200" || !repaired.WorkloadRunning() {
		t.Fatalf("workload state changed during repair: %q/%v", repaired.WorkloadVersion(), repaired.WorkloadRunning())
	}
}

func TestEnsureNetproxyDeploymentRepairsSpecOnceConcurrently(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	cfg := EnsureNetproxy(store, node.ID, "v0.0.200")
	broken := internaldeploy.NetproxySpec()
	broken.Container().Runtime.FileDescriptorLimit = apigen.Some[uint32](128)
	statetest.UpdateDeploymentSpecKeepingWorkload(store, apigen.Context{}, cfg.Deployment.ID, broken)
	brokenVersion := erru.Must(store.Queries().GetLatestDeployment(context.Background(), cfg.Deployment.ID)).Meta.SpecVersion

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { EnsureNetproxy(store, node.ID, "v0.0.200") })
	}
	wg.Wait()

	repaired := erru.Must(store.Queries().GetLatestDeployment(context.Background(), cfg.Deployment.ID))
	if repaired.Meta.SpecVersion != brokenVersion+1 {
		t.Fatalf("version = %d, want one repair above %d", repaired.Meta.SpecVersion, brokenVersion)
	}
}

func TestEnsureNetproxyDeploymentPreservesDesiredStateConcurrently(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	cfg := EnsureNetproxy(store, node.ID, "v0.0.200")
	statetest.SetDeploymentWorkloadState(store, apigen.Context{}, cfg.Deployment.ID, "v0.0.199", false)
	manualVersion := erru.Must(store.Queries().GetLatestDeployment(context.Background(), cfg.Deployment.ID)).Meta.SpecVersion

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { EnsureNetproxy(store, node.ID, "v0.0.201") })
	}
	wg.Wait()

	updated := erru.Must(store.Queries().GetLatestDeployment(context.Background(), cfg.Deployment.ID))
	if updated.Meta.SpecVersion != manualVersion {
		t.Fatalf("version = %d, want unchanged manual version %d", updated.Meta.SpecVersion, manualVersion)
	}
	if updated.WorkloadVersion() != "v0.0.199" || updated.WorkloadRunning() {
		t.Fatalf("workload state = %q/%v, want stopped v0.0.199", updated.WorkloadVersion(), updated.WorkloadRunning())
	}
}

func TestEnsureNetproxyDeploymentRequiresExplicitVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	defer func() {
		if recover() == nil {
			t.Fatal("EnsureNetproxyDeployment did not panic without version")
		}
	}()
	EnsureNetproxy(store, node.ID, "")
}

func TestEnsureNetproxyDeploymentPreservesExistingVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary", netip.MustParseAddr("10.0.0.1"))
	cfg := EnsureNetproxy(store, node.ID, "v0.0.200")

	again := EnsureNetproxy(store, node.ID, "v0.0.201")
	if again.WorkloadVersion() != "v0.0.200" {
		t.Fatalf("desired version = %q, want preserved v0.0.200", again.WorkloadVersion())
	}
	if again.Meta.SpecVersion != cfg.Meta.SpecVersion {
		t.Fatalf("version = %d, want unchanged %d", again.Meta.SpecVersion, cfg.Meta.SpecVersion)
	}
}
