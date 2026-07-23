package sqlite

import (
	"bytes"
	"context"
	"database/sql"
	"fmt"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestDeploymentV2MigrationSharedInitialization(t *testing.T) {
	openers := map[string]func(string) (*deploymentStore, func() error){
		"primary": func(path string) (*deploymentStore, func() error) {
			store := NewPrimaryStorage(path)
			return store.deploymentStore, store.Close
		},
		"secondary": func(path string) (*deploymentStore, func() error) {
			store := NewSecondaryStorage(path)
			return store.deploymentStore, store.Close
		},
	}

	for role, open := range openers {
		t.Run(role, func(t *testing.T) {
			dbPath := filepath.Join(t.TempDir(), role+".db")
			seedLegacyDeploymentConfigs(t, dbPath)

			store, closeStore := open(dbPath)
			assertMigratedDeploymentState(t, store)
			firstBlobs := deploymentSpecBlobs(t, store.db)
			assertDeploymentMigrationMarker(t, store.db)
			if err := closeStore(); err != nil {
				t.Fatal(err)
			}

			store, closeStore = open(dbPath)
			defer closeStore()
			assertMigratedDeploymentState(t, store)
			assertDeploymentMigrationMarker(t, store.db)
			secondBlobs := deploymentSpecBlobs(t, store.db)
			if !reflect.DeepEqual(firstBlobs, secondBlobs) {
				t.Fatalf("deployment blobs changed on repeated open\nfirst:  %v\nsecond: %v", firstBlobs, secondBlobs)
			}
		})
	}
}

func TestDeploymentV2MigrationRejectsInvalidMarker(t *testing.T) {
	db, err := sql.Open("sqlite", "file:"+filepath.Join(t.TempDir(), "invalid-marker.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`INSERT INTO local_kv (key, value) VALUES (?, ?)`, deploymentV2MigrationKey, []byte{2}); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if recover() == nil {
			t.Fatal("migration accepted an invalid marker")
		}
	}()
	migrateDeploymentConfigsV2(db)
}

func seedLegacyDeploymentConfigs(t *testing.T, dbPath string) {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+dbPath)
	if err != nil {
		t.Fatalf("open legacy database: %v", err)
	}
	defer db.Close()
	if _, err := db.Exec(schema); err != nil {
		t.Fatalf("create legacy schema: %v", err)
	}

	containerSpec := (&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{ContainerImage: &apigen.ContainerImageConfig{Image: "example/app"}},
		Runner: apigen.RunnerConfig{Container: apigen.ContainerRunnerConfig{
			AssetMounts: []*apigen.ContainerAssetMount{{
				AssetID:    42,
				Asset:      "legacy-asset-name",
				Path:       "/etc/app/config",
				Format:     "legacy-format",
				Executable: true,
			}},
		}},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL},
	}).Encode()
	systemSpec := (&apigen.DeploymentSpec{
		Prepare: apigen.PrepareConfig{GithubRelease: &apigen.GithubReleaseConfig{
			Repo:  "github.com/acme/opendeploy",
			Asset: "opendeploy-linux-amd64",
			Tag:   "legacy-tag",
		}},
		Runner: apigen.RunnerConfig{Systemd: apigen.SystemdRunnerConfig{
			Name:    "opendeploy",
			BinPath: "/var/lib/opendeploy/bin/opendeploy",
		}},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}).Encode()

	for _, row := range []struct {
		id, version, running int
		name, desired        string
		blob                 []byte
	}{
		{id: 1, version: 2, running: 1, name: "api", desired: "v2", blob: containerSpec},
		{id: 2, version: 1, running: 1, name: "system", desired: "v-system", blob: systemSpec},
	} {
		if _, err := db.Exec(`INSERT INTO deployment_configs
			(deployment_id, node_id, space_id, name, created_at, version, updated_at, spec_blob, desired_version, desired_running)
			VALUES (?, 7, 1, ?, 100, ?, 200, ?, ?, ?)`, row.id, row.name, row.version, row.blob, row.desired, row.running); err != nil {
			t.Fatalf("insert legacy current deployment %d: %v", row.id, err)
		}
	}
	for _, row := range []struct {
		id, version, running int
		desired              string
		blob                 []byte
	}{
		{id: 1, version: 1, desired: "v1", blob: containerSpec},
		{id: 1, version: 2, running: 1, desired: "v2", blob: containerSpec},
		{id: 2, version: 1, running: 1, desired: "v-system", blob: systemSpec},
	} {
		if _, err := db.Exec(`INSERT INTO deployment_config_history
			(deployment_id, version, updated_at, spec_blob, desired_version, desired_running)
			VALUES (?, ?, 200, ?, ?, ?)`, row.id, row.version, row.blob, row.desired, row.running); err != nil {
			t.Fatalf("insert legacy deployment history %d/%d: %v", row.id, row.version, err)
		}
	}
}

func assertMigratedDeploymentState(t *testing.T, store *deploymentStore) {
	t.Helper()
	container := store.configCache[1]
	if container == nil || container.WorkloadVersion() != "v2" || !container.WorkloadRunning() {
		t.Fatalf("migrated current container = %+v", container)
	}
	workload := container.Spec.Container()
	if workload == nil || workload.Source.RemoteImage == nil || workload.Source.RemoteImage.Image != "example/app" {
		t.Fatalf("migrated container source = %+v", workload)
	}
	if len(workload.Runtime.AssetMounts) != 1 {
		t.Fatalf("migrated asset mounts = %+v", workload.Runtime.AssetMounts)
	}
	mount := workload.Runtime.AssetMounts[0]
	if mount.AssetID != 42 || mount.ContainerPath != "/etc/app/config" || mount.Permission != apigen.FilePermission_READ_EXECUTE {
		t.Fatalf("migrated asset mount = %+v", mount)
	}

	system := store.configCache[2]
	if system == nil || system.WorkloadVersion() != "v-system" || !system.WorkloadRunning() || system.Spec.SystemdSpec == nil {
		t.Fatalf("migrated current system deployment = %+v", system)
	}
	if system.Spec.SystemdSpec.Source.Repo != "github.com/acme/opendeploy" {
		t.Fatalf("migrated system source = %+v", system.Spec.SystemdSpec.Source)
	}

	rows, err := store.q.ListDeploymentConfigHistory(context.Background(), 1)
	if err != nil {
		t.Fatalf("read migrated history: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("migrated history length = %d, want 2", len(rows))
	}
	for i, row := range rows {
		spec, err := apigen.DecodeDeploymentSpec2(row.SpecBlob)
		if err != nil {
			t.Fatalf("decode migrated history %d: %v", row.Version, err)
		}
		wantVersion := fmt.Sprintf("v%d", i+1)
		if spec.WorkloadVersion() != wantVersion || spec.WorkloadRunning() != (i == 1) {
			t.Fatalf("history %d workload = %q/%v, want %q/%v", row.Version, spec.WorkloadVersion(), spec.WorkloadRunning(), wantVersion, i == 1)
		}
	}

	for key, blob := range deploymentSpecBlobs(t, store.db) {
		spec, err := apigen.DecodeDeploymentSpec2(blob)
		if err != nil {
			t.Fatalf("decode migrated %s: %v", key, err)
		}
		if bytes.Contains(blob, []byte("legacy-asset-name")) || bytes.Contains(blob, []byte("legacy-format")) || bytes.Contains(blob, []byte("legacy-tag")) {
			t.Fatalf("migrated %s retained dropped V1 metadata", key)
		}
		if spec.WorkloadVersion() == "" {
			t.Fatalf("migrated %s lost workload version", key)
		}
	}
	assertDesiredMirrors(t, store.db)
}

func assertDesiredMirrors(t *testing.T, db *sql.DB) {
	t.Helper()
	for _, table := range []string{"deployment_configs", "deployment_config_history"} {
		rows, err := db.Query(`SELECT spec_blob, desired_version, desired_running FROM ` + table)
		if err != nil {
			t.Fatalf("read %s mirrors: %v", table, err)
		}
		for rows.Next() {
			var blob []byte
			var version string
			var running int
			if err := rows.Scan(&blob, &version, &running); err != nil {
				rows.Close()
				t.Fatalf("scan %s mirrors: %v", table, err)
			}
			spec, err := apigen.DecodeDeploymentSpec2(blob)
			if err != nil {
				rows.Close()
				t.Fatalf("decode %s mirrors: %v", table, err)
			}
			if version != spec.WorkloadVersion() || (running != 0) != spec.WorkloadRunning() {
				rows.Close()
				t.Fatalf("%s mirrors = %q/%d, workload = %q/%v", table, version, running, spec.WorkloadVersion(), spec.WorkloadRunning())
			}
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate %s mirrors: %v", table, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close %s mirror rows: %v", table, err)
		}
	}
}

func deploymentSpecBlobs(t *testing.T, db *sql.DB) map[string][]byte {
	t.Helper()
	out := make(map[string][]byte)
	for prefix, query := range map[string]string{
		"c": `SELECT deployment_id, version, spec_blob FROM deployment_configs ORDER BY deployment_id`,
		"h": `SELECT deployment_id, version, spec_blob FROM deployment_config_history ORDER BY deployment_id, version`,
	} {
		rows, err := db.Query(query)
		if err != nil {
			t.Fatalf("read %s deployment blobs: %v", prefix, err)
		}
		for rows.Next() {
			var id, version int
			var blob []byte
			if err := rows.Scan(&id, &version, &blob); err != nil {
				rows.Close()
				t.Fatalf("scan %s deployment blob: %v", prefix, err)
			}
			out[fmt.Sprintf("%s:%d:%d", prefix, id, version)] = blob
		}
		if err := rows.Err(); err != nil {
			rows.Close()
			t.Fatalf("iterate %s deployment blobs: %v", prefix, err)
		}
		if err := rows.Close(); err != nil {
			t.Fatalf("close %s deployment blobs: %v", prefix, err)
		}
	}
	return out
}

func assertDeploymentMigrationMarker(t *testing.T, db *sql.DB) {
	t.Helper()
	var marker []byte
	if err := db.QueryRow(`SELECT value FROM local_kv WHERE key = ?`, deploymentV2MigrationKey).Scan(&marker); err != nil {
		t.Fatalf("read deployment migration marker: %v", err)
	}
	if !bytes.Equal(marker, []byte{1}) {
		t.Fatalf("deployment migration marker = %v, want [1]", marker)
	}
}
