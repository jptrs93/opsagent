package sqlite

import (
	"database/sql"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// preSplitStatusSchema is the shape of scheduled_instance_status before the
// preparer status was split into stages: one rollup column, no stage columns.
const preSplitStatusSchema = `CREATE TABLE scheduled_instance_status (
	scheduled_instance_id   INTEGER NOT NULL,
	updated_at              INTEGER NOT NULL,
	deployment_id           INTEGER NOT NULL DEFAULT 0,
	preparer_config_version INTEGER,
	preparer_artifact       TEXT,
	preparer_status         INTEGER,
	runner_config_version   INTEGER,
	runner_pid              INTEGER,
	runner_artifact         TEXT,
	runner_status           INTEGER,
	runner_num_restarts     INTEGER,
	runner_last_restart_at  INTEGER,
	runner_extra_blob       BLOB NOT NULL DEFAULT x'',
	PRIMARY KEY (scheduled_instance_id, updated_at)
)`

// The migration has to rewrite real databases in place: back the legacy rollup
// into the two stage columns, then drop it. Every legacy value must come back
// out of Rollup() unchanged, or upgrading would silently change what an
// instance's recorded status means.
func TestPreparerStageBackfillMigration(t *testing.T) {
	for _, tc := range []struct {
		name       string
		migrations string
	}{
		{"primary", primaryMigrations},
		{"secondary", secondaryMigrations},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), "pre-split.db")
			db, err := sql.Open("sqlite", "file:"+dbPath)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = db.Close() }()
			if _, err := db.Exec(preSplitStatusSchema); err != nil {
				t.Fatal(err)
			}

			// One row per legacy rollup, plus a row with no preparer at all.
			legacy := []struct {
				instanceID int64
				status     sql.NullInt64
				want       apigen.PreparationStatus
			}{
				{1, sql.NullInt64{Int64: 2, Valid: true}, apigen.PreparationStatus_PREPARING},
				{2, sql.NullInt64{Int64: 3, Valid: true}, apigen.PreparationStatus_DOWNLOADING},
				{3, sql.NullInt64{Int64: 4, Valid: true}, apigen.PreparationStatus_READY},
				{4, sql.NullInt64{Int64: 5, Valid: true}, apigen.PreparationStatus_FAILED},
				{5, sql.NullInt64{Int64: 6, Valid: true}, apigen.PreparationStatus_PULLING},
				{6, sql.NullInt64{Int64: 0, Valid: true}, apigen.PreparationStatus_PREPARATION_STATUS_UNKNOWN},
			}
			for _, l := range legacy {
				if _, err := db.Exec(`INSERT INTO scheduled_instance_status
					(scheduled_instance_id, updated_at, preparer_config_version, preparer_artifact, preparer_status)
					VALUES (?, 100, 7, 'art', ?)`, l.instanceID, l.status); err != nil {
					t.Fatal(err)
				}
			}
			// A row that never had a preparer must stay absent, not become a
			// recorded UNKNOWN — the presence guard moved to config_version.
			if _, err := db.Exec(`INSERT INTO scheduled_instance_status
				(scheduled_instance_id, updated_at) VALUES (99, 100)`); err != nil {
				t.Fatal(err)
			}

			// Migrations re-run on every startup, so this must be idempotent.
			applyMigrations(db, tc.migrations)
			applyMigrations(db, tc.migrations)

			if err := db.QueryRow(`SELECT preparer_status FROM scheduled_instance_status LIMIT 1`).Scan(new(any)); err == nil {
				t.Fatal("preparer_status column still present after migration")
			}

			for _, l := range legacy {
				var inputs, image int64
				if err := db.QueryRow(`SELECT preparer_inputs_status, preparer_image_status
					FROM scheduled_instance_status WHERE scheduled_instance_id = ?`, l.instanceID).Scan(&inputs, &image); err != nil {
					t.Fatal(err)
				}
				staged := apigen.PreparerStatus{
					Inputs: apigen.InputsStatus(inputs),
					Image:  apigen.ImageStatus(image),
				}
				if got := staged.Rollup(); got != l.want {
					t.Errorf("legacy status %d backfilled to inputs=%v image=%v, re-derives to %v, want %v",
						l.status.Int64, staged.Inputs, staged.Image, got, l.want)
				}
			}

			var configVersion sql.NullInt64
			if err := db.QueryRow(`SELECT preparer_config_version FROM scheduled_instance_status
				WHERE scheduled_instance_id = 99`).Scan(&configVersion); err != nil {
				t.Fatal(err)
			}
			if configVersion.Valid {
				t.Fatal("row without a preparer gained a config version")
			}
		})
	}
}

// A database migrated in place must then be usable by the running code, not
// merely structurally correct. This builds a real store, rewinds just its status
// table to the pre-split shape, and reopens — so the instance rows, pinned
// config, and local caches around the migrated column are all genuine.
func TestSecondaryOpensMigratedPreSplitDatabase(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "rewound.db")
	store := NewSecondaryStorage(dbPath)
	const instanceID int32 = 11
	store.MustWriteScheduledInstanceAssignment(&apigen.ScheduledInstanceState{
		Instance: apigen.ScheduledInstance{
			ID:                instanceID,
			NodeID:            23,
			DeploymentID:      7,
			DeploymentVersion: 3,
			State:             apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING,
		},
		Config: apigen.DeploymentConfig{
			ID: 7, NodeID: 23, Version: 3,
			Identity: apigen.DeploymentIdentity{SpaceID: 1, Name: "api"},
			Spec:     *testSpecWithState("v3", true),
		},
	})
	store.MustWriteScheduledInstanceStatus(instanceID, func(s *apigen.ScheduledInstanceStatus) bool {
		s.BumpUpdatedAt()
		s.Preparer = apigen.PreparerStatus{
			DeploymentConfigVersion: 3,
			Artifact:                "art",
			Inputs:                  apigen.InputsStatus_INPUTS_READY,
			Image:                   apigen.ImageStatus_IMAGE_READY,
		}
		return true
	})
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Rewind to the pre-split shape: a single rollup column, no stages.
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`ALTER TABLE scheduled_instance_status ADD COLUMN preparer_status INTEGER`,
		`UPDATE scheduled_instance_status SET preparer_status = 4 WHERE preparer_config_version IS NOT NULL`,
		`ALTER TABLE scheduled_instance_status DROP COLUMN preparer_inputs_status`,
		`ALTER TABLE scheduled_instance_status DROP COLUMN preparer_image_status`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("rewinding schema (%s): %v", stmt, err)
		}
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}

	reopened := NewSecondaryStorage(dbPath)
	t.Cleanup(func() { _ = reopened.Close() })

	got := reopened.FetchScheduledInstanceStatus(instanceID)
	if got == nil {
		t.Fatal("migrated legacy status was not readable")
	}
	if got.Preparer.Rollup() != apigen.PreparationStatus_READY {
		t.Fatalf("legacy READY row now rolls up to %v", got.Preparer.Rollup())
	}
	if got.Preparer.Artifact != "art" {
		t.Fatalf("artifact = %q, want it preserved across the migration", got.Preparer.Artifact)
	}
}
