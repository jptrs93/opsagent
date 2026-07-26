package sqlite

import (
	"database/sql"
	"path/filepath"
	"sort"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// legacyDeployment is one row of the pre-migration fixture: a deployment_configs
// row plus the single mutable deployment_status row the old schema kept for it.
type legacyDeployment struct {
	id        int32
	nodeID    int32
	name      string
	version   int32
	running   bool
	deleted   bool
	statusAt  int64 // HLC nanos; 0 is the old "nothing observed yet" placeholder
	runnerVer int32
}

const (
	legacyHostRunning    int32 = 4
	legacySecondRunning  int32 = 7
	legacyStopped        int32 = 9
	legacyDeleted        int32 = 11
	legacyNeverReported  int32 = 13
	legacyMigratedNodeID int32 = 2
)

// legacyFixture covers every shape the migration has to decide about: two
// ordinary running deployments, a stopped one, a soft-deleted one, and one that
// is running but has never reported a status.
func legacyFixture() []legacyDeployment {
	return []legacyDeployment{
		{id: legacyHostRunning, nodeID: legacyMigratedNodeID, name: "api", version: 3, running: true, statusAt: 1_700_000_000_000_000_001, runnerVer: 3},
		{id: legacySecondRunning, nodeID: legacyMigratedNodeID, name: "worker", version: 1, running: true, statusAt: 1_700_000_000_000_000_002, runnerVer: 1},
		{id: legacyStopped, nodeID: legacyMigratedNodeID, name: "batch", version: 5, running: false, statusAt: 1_700_000_000_000_000_003, runnerVer: 5},
		{id: legacyDeleted, nodeID: legacyMigratedNodeID, name: "old", version: 2, running: true, deleted: true, statusAt: 1_700_000_000_000_000_004, runnerVer: 2},
		{id: legacyNeverReported, nodeID: legacyMigratedNodeID, name: "fresh", version: 1, running: true},
	}
}

// seedLegacyDB writes the fixture straight into the retained legacy tables
// without going through a store, so the migration sees exactly what an upgraded
// cluster's database looks like.
func seedLegacyDB(t *testing.T, dbPath string, deployments []legacyDeployment) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	for _, d := range deployments {
		spec := testSpecWithState("v"+d.name, d.running)
		deleted := 0
		if d.deleted {
			deleted = 1
		}
		if _, err := db.Exec(`
INSERT INTO deployment_configs (deployment_id, version, node_id, space_id, name, created_at, updated_at, updated_by, spec_blob, deleted)
VALUES (?, ?, ?, 1, ?, 500, 1000, 0, ?, ?)`,
			d.id, d.version, d.nodeID, d.name, spec.Encode(), deleted); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(`
INSERT INTO deployment_status (deployment_id, updated_at, preparer_config_version, preparer_artifact, preparer_status,
                               runner_config_version, runner_pid, runner_artifact, runner_status,
                               runner_num_restarts, runner_last_restart_at, runner_extra_blob)
VALUES (?, ?, ?, 'artifact', ?, ?, 4242, 'artifact', ?, 2, 900, x'')`,
			d.id, d.statusAt, d.runnerVer, int64(apigen.PreparationStatus_READY),
			d.runnerVer, int64(apigen.RunningStatus_RUNNING)); err != nil {
			t.Fatal(err)
		}
	}
}

func migratedInstanceIDs(t *testing.T, db *sql.DB) []int32 {
	t.Helper()
	rows, err := db.Query(`SELECT id FROM scheduled_instances ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := make([]int32, 0)
	for rows.Next() {
		var id int32
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		out = append(out, id)
	}
	return out
}

func TestMigrationCreatesOneServingInstancePerRunningDeployment(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	seedLegacyDB(t, dbPath, legacyFixture())

	store := NewPrimaryStorage(dbPath)
	defer store.Close()

	got := migratedInstanceIDs(t, store.db)
	want := []int32{legacyHostRunning, legacySecondRunning, legacyNeverReported}
	if len(got) != len(want) {
		t.Fatalf("migrated instances = %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("migrated instances = %v, want %v", got, want)
		}
	}

	// The scheduled instance id must be the deployment id: the primary and the
	// secondary migrate with no coordination and only agree because of this.
	for _, id := range want {
		inst := store.FetchScheduledInstance(id)
		if inst == nil {
			t.Fatalf("no instance for deployment %d", id)
		}
		if inst.ID != inst.DeploymentID {
			t.Fatalf("instance %d has deployment id %d, want them equal", inst.ID, inst.DeploymentID)
		}
		if inst.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
			t.Fatalf("instance %d state = %v, want RUN_SERVING", id, inst.State)
		}
		if inst.InstanceOrdinal != migratedInstanceOrdinal {
			t.Fatalf("instance %d ordinal = %d, want %d", id, inst.InstanceOrdinal, migratedInstanceOrdinal)
		}
		if inst.NodeID != legacyMigratedNodeID {
			t.Fatalf("instance %d node = %d, want %d", id, inst.NodeID, legacyMigratedNodeID)
		}
	}

	for _, d := range legacyFixture() {
		if d.id == legacyHostRunning || d.id == legacySecondRunning || d.id == legacyNeverReported {
			continue
		}
		if inst := store.FetchScheduledInstance(d.id); inst != nil {
			t.Fatalf("deployment %d should not have been migrated, got %+v", d.id, inst)
		}
	}
}

// The pinned config version is what containerID is built from, so a placement
// migrated at the wrong version would have the operator create a container over
// the id of the one already running.
func TestMigrationPinsVersionsAndCarriesStatus(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	seedLegacyDB(t, dbPath, legacyFixture())

	store := NewPrimaryStorage(dbPath)
	defer store.Close()

	for _, d := range legacyFixture() {
		if !d.running || d.deleted {
			continue
		}
		inst := store.FetchScheduledInstance(d.id)
		if inst == nil {
			t.Fatalf("no instance for deployment %d", d.id)
		}
		if inst.DeploymentVersion != d.version {
			t.Fatalf("instance %d pinned version = %d, want %d", d.id, inst.DeploymentVersion, d.version)
		}

		history := store.MustFetchScheduledInstanceStatusHistory(d.id)
		if d.statusAt == 0 {
			// A placeholder row is not a status: migrating it would tell the
			// operator to reattach to a container that was never started.
			if len(history) != 0 {
				t.Fatalf("deployment %d has no legacy status, got %d migrated rows", d.id, len(history))
			}
			continue
		}
		if len(history) != 1 {
			t.Fatalf("deployment %d migrated status rows = %d, want 1", d.id, len(history))
		}
		st := history[0]
		if st.ScheduledInstanceID != d.id || st.DeploymentID != d.id {
			t.Fatalf("status ids = %d/%d, want %d", st.ScheduledInstanceID, st.DeploymentID, d.id)
		}
		if st.UpdatedAt.UnixNano() != d.statusAt {
			t.Fatalf("status clock = %d, want %d preserved verbatim", st.UpdatedAt.UnixNano(), d.statusAt)
		}
		if st.Runner.DeploymentConfigVersion != d.runnerVer {
			t.Fatalf("runner config version = %d, want %d", st.Runner.DeploymentConfigVersion, d.runnerVer)
		}
		if st.Runner.Status != apigen.RunningStatus_RUNNING {
			t.Fatalf("runner status = %v, want RUNNING", st.Runner.Status)
		}
		if st.Runner.NumberOfRestarts != 2 {
			t.Fatalf("restart count = %d, want 2 carried over", st.Runner.NumberOfRestarts)
		}
		if st.Preparer.Status != apigen.PreparationStatus_READY || st.Preparer.Artifact != "artifact" {
			t.Fatalf("preparer not carried over: %+v", st.Preparer)
		}
	}
}

// The migration inserts explicit ids, so sqlite_sequence has to have been
// carried past them or the next instance created would collide with a migrated
// one.
func TestMigrationLeavesAutoincrementAboveMigratedIDs(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	seedLegacyDB(t, dbPath, legacyFixture())

	store := NewPrimaryStorage(dbPath)
	defer store.Close()

	inst := store.CreateScheduledInstance(legacyHostRunning, 4, legacyMigratedNodeID, 1,
		apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY)
	if inst.ID <= legacyNeverReported {
		t.Fatalf("new instance id = %d, want above the highest migrated id %d", inst.ID, legacyNeverReported)
	}
}

func TestSecondaryMigrationSeedsLocalAssignmentCache(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	seedLegacyDB(t, dbPath, legacyFixture())

	store := NewSecondaryStorage(dbPath)
	defer store.Close()

	snapshot := store.FetchScheduledSnapshot(nil)
	if len(snapshot) != 3 {
		t.Fatalf("local assignments = %d, want 3", len(snapshot))
	}
	byID := map[int32]apigen.ScheduledInstanceState{}
	for _, item := range snapshot {
		byID[item.Instance.ID] = item
	}

	running := byID[legacyHostRunning]
	if running.Instance.ID == 0 {
		t.Fatalf("no local assignment for deployment %d", legacyHostRunning)
	}
	// The operator runs off the config embedded in the blob, not the legacy
	// deployment_configs row, so the embedded copy has to be complete.
	if running.Config.ID != legacyHostRunning || running.Config.Version != 3 {
		t.Fatalf("embedded config = %+v, want deployment %d at version 3", running.Config, legacyHostRunning)
	}
	if running.Config.Identity.Name != "api" || running.Config.NodeID != legacyMigratedNodeID {
		t.Fatalf("embedded identity/node not carried over: %+v node=%d", running.Config.Identity, running.Config.NodeID)
	}
	if !running.Config.WorkloadRunning() {
		t.Fatalf("embedded config should be running")
	}
	if running.Status.Runner.DeploymentConfigVersion != 3 || running.Status.Runner.Status != apigen.RunningStatus_RUNNING {
		t.Fatalf("embedded status = %+v, want RUNNING at version 3", running.Status.Runner)
	}
}

// scheduled_instances is the primary's table: assignments are minted there and
// reach a worker only over the cluster stream. Rows written on a secondary are
// inert — loadLocalScheduledInstanceCache replaces whatever loadCache built from
// them — but they would linger as a second, stale account of what the node is
// running long after those assignments were retired.
func TestSecondaryMigrationLeavesTheAssignmentTableAlone(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	seedLegacyDB(t, dbPath, legacyFixture())

	store := NewSecondaryStorage(dbPath)
	defer store.Close()

	if ids := migratedInstanceIDs(t, store.db); len(ids) != 0 {
		t.Fatalf("secondary wrote scheduled_instances rows %v", ids)
	}
	if len(store.FetchScheduledSnapshot(nil)) != 3 {
		t.Fatal("secondary should still hold its assignments in the local cache")
	}
}

// Both sides derive ids with no coordination. If they ever disagree, the
// primary's snapshot prunes everything the secondary holds and the whole
// cluster's workloads are recreated.
func TestPrimaryAndSecondaryMigrationsAgreeOnInstanceIDs(t *testing.T) {
	dir := t.TempDir()
	primaryPath := filepath.Join(dir, "primary.db")
	secondaryPath := filepath.Join(dir, "secondary.db")
	seedLegacyDB(t, primaryPath, legacyFixture())
	seedLegacyDB(t, secondaryPath, legacyFixture())

	primary := NewPrimaryStorage(primaryPath)
	defer primary.Close()
	secondary := NewSecondaryStorage(secondaryPath)
	defer secondary.Close()

	primaryIDs := migratedInstanceIDs(t, primary.db)

	secondaryIDs := make([]int32, 0)
	for _, item := range secondary.FetchScheduledSnapshot(nil) {
		secondaryIDs = append(secondaryIDs, item.Instance.ID)
	}
	sort.Slice(secondaryIDs, func(i, j int) bool { return secondaryIDs[i] < secondaryIDs[j] })

	if len(primaryIDs) != len(secondaryIDs) {
		t.Fatalf("primary ids %v vs secondary ids %v", primaryIDs, secondaryIDs)
	}
	for i := range primaryIDs {
		if primaryIDs[i] != secondaryIDs[i] {
			t.Fatalf("primary ids %v vs secondary ids %v", primaryIDs, secondaryIDs)
		}
	}
}

func TestMigrationRunsOnlyOnce(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	seedLegacyDB(t, dbPath, legacyFixture())

	store := NewPrimaryStorage(dbPath)
	first := migratedInstanceIDs(t, store.db)
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = NewPrimaryStorage(dbPath)
	defer store.Close()
	second := migratedInstanceIDs(t, store.db)
	if len(second) != len(first) {
		t.Fatalf("reopening migrated again: %v then %v", first, second)
	}
}

// An empty assignment cache is a normal steady state once everything has been
// finalized. Keying the guard off emptiness rather than a marker would resurrect
// retired assignments from the retained legacy tables on the next boot.
func TestSecondaryMigrationDoesNotResurrectFinalizedAssignments(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "secondary.db")
	seedLegacyDB(t, dbPath, legacyFixture())

	store := NewSecondaryStorage(dbPath)
	for _, item := range store.FetchScheduledSnapshot(nil) {
		final := item
		final.Instance.State = apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_FINALIZED
		store.MustWriteScheduledInstanceAssignment(&final)
	}
	if remaining := store.FetchScheduledSnapshot(nil); len(remaining) != 0 {
		t.Fatalf("expected every assignment finalized, %d remain", len(remaining))
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	store = NewSecondaryStorage(dbPath)
	defer store.Close()
	if revived := store.FetchScheduledSnapshot(nil); len(revived) != 0 {
		t.Fatalf("migration resurrected %d finalized assignments", len(revived))
	}
}

// A database that has never held the legacy tables' contents must still come up
// clean, and must not re-run the migration on every boot.
func TestMigrationOnFreshDatabaseIsANoOp(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	store := NewPrimaryStorage(dbPath)
	defer store.Close()

	if ids := migratedInstanceIDs(t, store.db); len(ids) != 0 {
		t.Fatalf("fresh database produced instances %v", ids)
	}
	if _, ok := store.FetchLocalKV(localKVScheduledInstanceMigration); !ok {
		t.Fatal("fresh database should still record the migration as applied")
	}
}

// createdAt comes from the config's updated_at: when this version was deployed
// is the closest thing the old schema recorded to when the placement started.
func TestMigrationDatesInstancesFromConfigUpdate(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "primary.db")
	seedLegacyDB(t, dbPath, legacyFixture())

	store := NewPrimaryStorage(dbPath)
	defer store.Close()

	inst := store.FetchScheduledInstance(legacyHostRunning)
	if inst == nil {
		t.Fatalf("no instance for deployment %d", legacyHostRunning)
	}
	if !inst.CreatedAt.Equal(time.UnixMilli(1000)) {
		t.Fatalf("instance created at %v, want the config's update time", inst.CreatedAt)
	}
}
