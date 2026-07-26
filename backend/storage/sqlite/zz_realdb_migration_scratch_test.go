package sqlite

// TEMPORARY scratch harness: runs the scheduled instance migration against
// snapshots of real cluster databases. Not part of the suite; delete after use.

import (
	"database/sql"
	"fmt"
	"os"
	"sort"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func legacyRows(t *testing.T, path string) map[int32]struct {
	version   int32
	nodeID    int32
	name      string
	deleted   bool
	running   bool
	clock     int64
	runnerVer int32
} {
	t.Helper()
	type row = struct {
		version   int32
		nodeID    int32
		name      string
		deleted   bool
		running   bool
		clock     int64
		runnerVer int32
	}
	db, err := sql.Open("sqlite", "file:"+path+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	out := map[int32]row{}
	rs, err := db.Query(`SELECT c.deployment_id, c.version, c.node_id, c.name, c.deleted, c.spec_blob,
	  COALESCE(s.updated_at,0), COALESCE(s.runner_config_version,0)
	  FROM deployment_configs c LEFT JOIN deployment_status s ON s.deployment_id = c.deployment_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rs.Close()
	for rs.Next() {
		var id, version, nodeID, runnerVer int32
		var name string
		var deleted int
		var blob []byte
		var clock int64
		if err := rs.Scan(&id, &version, &nodeID, &name, &deleted, &blob, &clock, &runnerVer); err != nil {
			t.Fatal(err)
		}
		spec, err := apigen.DecodeDeploymentSpec(blob)
		if err != nil {
			t.Fatalf("deployment %d spec decode: %v", id, err)
		}
		out[id] = row{version, nodeID, name, deleted != 0, spec.WorkloadRunning(), clock, runnerVer}
	}
	return out
}

func TestZZRealDBMigration(t *testing.T) {
	dir := os.Getenv("REAL_DB_DIR")
	if dir == "" {
		t.Skip("REAL_DB_DIR not set")
	}
	primaryPath := dir + "/run/primary.db"
	secondaryPath := dir + "/run/secondary.db"

	legacyPrimary := legacyRows(t, primaryPath)
	legacySecondary := legacyRows(t, secondaryPath)

	primary := NewPrimaryStorage(primaryPath)
	defer primary.Close()
	secondary := NewSecondaryStorage(secondaryPath)
	defer secondary.Close()

	// --- primary ---
	rows, err := primary.db.Query(`SELECT id, deployment_id, deployment_version, node_id, instance_ordinal, state FROM scheduled_instances ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	fmt.Println("=== PRIMARY migrated instances ===")
	fmt.Printf("%-5s %-6s %-8s %-5s %-8s %-8s %s\n", "id", "dep", "version", "node", "ordinal", "state", "name")
	primaryByNode := map[int32][]int32{}
	migrated := map[int32]bool{}
	for rows.Next() {
		var id, dep, version, node, ordinal, state int32
		if err := rows.Scan(&id, &dep, &version, &node, &ordinal, &state); err != nil {
			t.Fatal(err)
		}
		lr := legacyPrimary[dep]
		fmt.Printf("%-5d %-6d %-8d %-5d %-8d %-8d %s\n", id, dep, version, node, ordinal, state, lr.name)
		migrated[dep] = true
		primaryByNode[node] = append(primaryByNode[node], id)

		if id != dep {
			t.Errorf("instance id %d != deployment id %d", id, dep)
		}
		if version != lr.version {
			t.Errorf("deployment %d migrated at version %d, live config is %d", dep, version, lr.version)
		}
		if state != int32(apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING) {
			t.Errorf("deployment %d migrated in state %d, want RUN_SERVING", dep, state)
		}
		if lr.deleted || lr.nodeID <= 0 || !lr.running {
			t.Errorf("deployment %d should not have migrated (deleted=%v node=%d running=%v)", dep, lr.deleted, lr.nodeID, lr.running)
		}
	}

	fmt.Println("\n=== PRIMARY skipped ===")
	skipped := make([]int32, 0)
	for id := range legacyPrimary {
		if !migrated[id] {
			skipped = append(skipped, id)
		}
	}
	sort.Slice(skipped, func(i, j int) bool { return skipped[i] < skipped[j] })
	for _, id := range skipped {
		lr := legacyPrimary[id]
		reason := "not running"
		if lr.deleted {
			reason = "deleted"
		} else if lr.nodeID <= 0 {
			reason = "no node"
		}
		fmt.Printf("  %-3d %-24s %s\n", id, lr.name, reason)
		if !lr.deleted && lr.nodeID > 0 && lr.running {
			t.Errorf("deployment %d was running and should have migrated", id)
		}
	}

	// --- status carry-over ---
	fmt.Println("\n=== PRIMARY status carry-over ===")
	for dep := range migrated {
		lr := legacyPrimary[dep]
		history := primary.MustFetchScheduledInstanceStatusHistory(dep)
		if lr.clock == 0 {
			if len(history) != 0 {
				t.Errorf("deployment %d had no legacy status but got %d rows", dep, len(history))
			}
			continue
		}
		if len(history) != 1 {
			t.Errorf("deployment %d status rows = %d, want 1", dep, len(history))
			continue
		}
		st := history[0]
		if st.UpdatedAt.UnixNano() != lr.clock {
			t.Errorf("deployment %d clock %d != legacy %d", dep, st.UpdatedAt.UnixNano(), lr.clock)
		}
		if st.Runner.DeploymentConfigVersion != lr.runnerVer {
			t.Errorf("deployment %d runner version %d != legacy %d", dep, st.Runner.DeploymentConfigVersion, lr.runnerVer)
		}
		// containerID is built from (deployment, runner config version); a mismatch
		// against the live config means the operator would not adopt the container.
		if st.Runner.DeploymentConfigVersion != lr.version {
			fmt.Printf("  NOTE dep %-3d %-24s runner at v%d but config at v%d\n", dep, lr.name, st.Runner.DeploymentConfigVersion, lr.version)
		}
	}

	// --- secondary ---
	fmt.Println("\n=== SECONDARY local assignments ===")
	secondaryIDs := make([]int32, 0)
	for _, item := range secondary.FetchScheduledSnapshot(nil) {
		secondaryIDs = append(secondaryIDs, item.Instance.ID)
		lr := legacySecondary[item.Instance.DeploymentID]
		fmt.Printf("  inst %-4d dep %-4d v%-4d node %-3d %-20s runnerV%d status=%v\n",
			item.Instance.ID, item.Instance.DeploymentID, item.Instance.DeploymentVersion,
			item.Instance.NodeID, lr.name, item.Status.Runner.DeploymentConfigVersion, item.Status.Runner.Status)
		if item.Config.ID != item.Instance.DeploymentID || item.Config.Version != item.Instance.DeploymentVersion {
			t.Errorf("instance %d embedded config mismatch: %+v", item.Instance.ID, item.Config)
		}
		if !item.Config.WorkloadRunning() {
			t.Errorf("instance %d embedded config is not running", item.Instance.ID)
		}
	}
	sort.Slice(secondaryIDs, func(i, j int) bool { return secondaryIDs[i] < secondaryIDs[j] })

	if n := migratedInstanceIDs(t, secondary.db); len(n) != 0 {
		t.Errorf("secondary wrote scheduled_instances rows: %v", n)
	}

	// --- the property that matters: both sides agree for the shared node ---
	node1 := primaryByNode[1]
	sort.Slice(node1, func(i, j int) bool { return node1[i] < node1[j] })
	fmt.Printf("\n=== AGREEMENT for node 1 ===\nprimary:   %v\nsecondary: %v\n", node1, secondaryIDs)
	if len(node1) != len(secondaryIDs) {
		t.Fatalf("primary node-1 instances %v != secondary %v", node1, secondaryIDs)
	}
	for i := range node1 {
		if node1[i] != secondaryIDs[i] {
			t.Fatalf("primary node-1 instances %v != secondary %v", node1, secondaryIDs)
		}
	}

	// --- next minted id must clear every migrated one ---
	next := primary.CreateScheduledInstance(1, 1, 2, 9,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)
	highest := int32(0)
	for dep := range migrated {
		if dep > highest {
			highest = dep
		}
	}
	fmt.Printf("\nnext minted instance id = %d (highest migrated = %d)\n", next.ID, highest)
	if next.ID <= highest {
		t.Errorf("next id %d collides with migrated range (highest %d)", next.ID, highest)
	}
}
