package deployments

import (
	"context"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"path/filepath"
	"sync"
	"testing"

	"github.com/jptrs93/goutil/erru"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

const netproxyFileDescriptorLimit = 65_536

func TestEnsureSystemDeploymentRepairsExistingSpec(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	created := statetest.MustCreateDeploymentForNode(store, apigen.Context{}, internaldeploy.SpaceID, internaldeploy.SelfName, node.ID, statetest.SpecWithState("", false))
	statetest.SetDeploymentWorkloadState(store, apigen.Context{}, created.DeploymentID, "v0.0.194", true)

	EnsureSystem(store, node.ID, "v0.0.195")

	var repaired *apigen.DeploymentEvent
	for _, cfg := range erru.Must(store.Queries().ListActiveDeployments(context.Background())) {
		if cfg.DeploymentID == created.DeploymentID {
			repaired = cfg
			break
		}
	}
	if repaired == nil {
		t.Fatal("repaired deployment not found")
	}
	if repaired.SpecVersion <= created.SpecVersion {
		t.Fatalf("version = %d, want repaired version above %d", repaired.SpecVersion, created.SpecVersion)
	}
	if !internaldeploy.IsSelfSpec(&repaired.Value.Spec) {
		t.Fatalf("spec was not repaired: %+v", repaired.Value.Spec)
	}
	if repaired.WorkloadVersion() != "v0.0.194" || !repaired.WorkloadRunning() {
		t.Fatalf("workload state = %q/%v, want preserved running v0.0.194", repaired.WorkloadVersion(), repaired.WorkloadRunning())
	}
}

func TestEnsureNetproxyDeploymentCreatesInternalConfig(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := EnsureNetproxy(store, node.ID, "v0.0.200")
	if cfg == nil {
		t.Fatal("netproxy config not returned")
	}
	if cfg.Value.NodeID != node.ID || cfg.Value.SpaceID != internaldeploy.SpaceID || cfg.Value.Name != internaldeploy.NetproxyName {
		t.Fatalf("unexpected config identity: node=%d space=%d name=%q", cfg.Value.NodeID, cfg.Value.SpaceID, cfg.Value.Name)
	}
	if !internaldeploy.IsNetproxyConfig(cfg) || !internaldeploy.IsInternalConfig(cfg) {
		t.Fatalf("netproxy config not recognized as internal: space=%d name=%q", cfg.Value.SpaceID, cfg.Value.Name)
	}
	if !cfg.WorkloadRunning() || cfg.WorkloadVersion() != "v0.0.200" {
		t.Fatalf("workload state = %q/%v, want running v0.0.200", cfg.WorkloadVersion(), cfg.WorkloadRunning())
	}
	if cfg.Value.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL || len(cfg.Value.Spec.Networking.PortForwarding) != 0 {
		t.Fatalf("unexpected networking config: %+v", cfg.Value.Spec.Networking)
	}
	if got := cfg.Value.Spec.Container().Runtime.FileDescriptorLimit; got != netproxyFileDescriptorLimit {
		t.Fatalf("file descriptor limit = %d, want %d", got, netproxyFileDescriptorLimit)
	}
	again := EnsureNetproxy(store, node.ID, "v0.0.200")
	if again.SpecVersion != cfg.SpecVersion {
		t.Fatalf("ensure bumped unchanged netproxy version from %d to %d", cfg.SpecVersion, again.SpecVersion)
	}
}

func TestInternalDeploymentsAreScopedByNodeID(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	nodeA := nodes.EnsurePrimaryNode(store, "node-a", "node-a")
	nodeB := nodes.EnsurePrimaryNode(store, "node-b", "node-b")

	a := EnsureNetproxy(store, nodeA.ID, "v0.0.200")
	b := EnsureNetproxy(store, nodeB.ID, "v0.0.200")
	if a.DeploymentID == b.DeploymentID || a.Value.NodeID != nodeA.ID || b.Value.NodeID != nodeB.ID {
		t.Fatalf("netproxy deployments not scoped by node: a=%+v b=%+v", a, b)
	}
	if a.Value.SpaceID != b.Value.SpaceID || a.Value.Name != b.Value.Name {
		t.Fatalf("internal identities differ across nodes: a=%d/%q b=%d/%q", a.Value.SpaceID, a.Value.Name, b.Value.SpaceID, b.Value.Name)
	}
}

func TestEnsureNetproxyDeploymentRepairsExistingSpec(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := EnsureNetproxy(store, node.ID, "v0.0.200")
	broken := internaldeploy.NetproxySpec()
	broken.Container().Runtime.FileDescriptorLimit = 128
	statetest.UpdateDeploymentSpecKeepingWorkload(store, apigen.Context{}, cfg.DeploymentID, broken)
	brokenVersion := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(cfg.DeploymentID))).SpecVersion

	repaired := EnsureNetproxy(store, node.ID, "v0.0.201")
	if repaired.SpecVersion <= brokenVersion {
		t.Fatalf("version = %d, want above broken version %d", repaired.SpecVersion, brokenVersion)
	}
	if got := repaired.Value.Spec.Container().Runtime.FileDescriptorLimit; got != netproxyFileDescriptorLimit {
		t.Fatalf("file descriptor limit = %d, want %d", got, netproxyFileDescriptorLimit)
	}
	if repaired.WorkloadVersion() != "v0.0.200" || !repaired.WorkloadRunning() {
		t.Fatalf("workload state changed during repair: %q/%v", repaired.WorkloadVersion(), repaired.WorkloadRunning())
	}
}

func TestEnsureNetproxyDeploymentRepairsSpecOnceConcurrently(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := EnsureNetproxy(store, node.ID, "v0.0.200")
	broken := internaldeploy.NetproxySpec()
	broken.Container().Runtime.FileDescriptorLimit = 128
	statetest.UpdateDeploymentSpecKeepingWorkload(store, apigen.Context{}, cfg.DeploymentID, broken)
	brokenVersion := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(cfg.DeploymentID))).SpecVersion

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { EnsureNetproxy(store, node.ID, "v0.0.200") })
	}
	wg.Wait()

	repaired := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(cfg.DeploymentID)))
	if repaired.SpecVersion != brokenVersion+1 {
		t.Fatalf("version = %d, want one repair above %d", repaired.SpecVersion, brokenVersion)
	}
}

func TestEnsureNetproxyDeploymentPreservesDesiredStateConcurrently(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := EnsureNetproxy(store, node.ID, "v0.0.200")
	statetest.SetDeploymentWorkloadState(store, apigen.Context{}, cfg.DeploymentID, "v0.0.199", false)
	manualVersion := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(cfg.DeploymentID))).SpecVersion

	var wg sync.WaitGroup
	for range 8 {
		wg.Go(func() { EnsureNetproxy(store, node.ID, "v0.0.201") })
	}
	wg.Wait()

	updated := erru.Must(store.Queries().GetLatestDeploymentEvent(context.Background(), int64(cfg.DeploymentID)))
	if updated.SpecVersion != manualVersion {
		t.Fatalf("version = %d, want unchanged manual version %d", updated.SpecVersion, manualVersion)
	}
	if updated.WorkloadVersion() != "v0.0.199" || updated.WorkloadRunning() {
		t.Fatalf("workload state = %q/%v, want stopped v0.0.199", updated.WorkloadVersion(), updated.WorkloadRunning())
	}
}

func TestEnsureNetproxyDeploymentRequiresExplicitVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	defer func() {
		if recover() == nil {
			t.Fatal("EnsureNetproxyDeployment did not panic without version")
		}
	}()
	EnsureNetproxy(store, node.ID, "")
}

func TestEnsureNetproxyDeploymentPreservesExistingVersion(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	node := nodes.EnsurePrimaryNode(store, "primary", "primary")
	cfg := EnsureNetproxy(store, node.ID, "v0.0.200")

	again := EnsureNetproxy(store, node.ID, "v0.0.201")
	if again.WorkloadVersion() != "v0.0.200" {
		t.Fatalf("desired version = %q, want preserved v0.0.200", again.WorkloadVersion())
	}
	if again.SpecVersion != cfg.SpecVersion {
		t.Fatalf("version = %d, want unchanged %d", again.SpecVersion, cfg.SpecVersion)
	}
}
