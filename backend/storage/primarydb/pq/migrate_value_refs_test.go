package pq

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"log/slog"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func wireVarintField(num, v uint64) []byte {
	return binary.AppendUvarint(appendTag(nil, num, wireVarint), v)
}

func wireStringField(num uint64, s string) []byte {
	return appendBytesField(nil, num, []byte(s))
}

func wireMessage(num uint64, fields ...[]byte) []byte {
	return appendBytesField(nil, num, bytes.Join(fields, nil))
}

func seedValueRows(t *testing.T, q *Queries) {
	t.Helper()
	db := q.sqlDB()
	stmts := []string{
		`INSERT INTO secret_event_log (id, global_seq, event_time, created_time, author, secret_id, version, value_version, value_changed, name, value_directory_id, space_id, smk_version, ciphertext, nonce, event_type)
		 VALUES (11, 1, 1, 1, 0, 3, 1, 1, 1, 'token', 0, 1, 1, x'00', x'00', 1),
		        (12, 2, 2, 1, 0, 3, 2, 1, 0, 'token2', 0, 1, 1, x'00', x'00', 2),
		        (13, 3, 3, 1, 0, 3, 3, 2, 1, 'token2', 0, 1, 1, x'01', x'01', 2)`,
		`INSERT INTO config_event_log (id, global_seq, event_time, created_time, author, config_id, version, value_version, value_changed, name, value_directory_id, space_id, value, event_type)
		 VALUES (21, 4, 4, 4, 0, 5, 1, 1, 1, 'level', 0, 1, 'debug', 1)`,
		`INSERT INTO asset_event_log (id, global_seq, event_time, created_time, author, asset_id, version, value_version, value_changed, key, asset_directory_id, space_id, size_bytes, sha256, event_type)
		 VALUES (31, 5, 5, 5, 0, 7, 1, 1, 1, 'app.conf', 0, 1, 1, 'a', 1),
		        (32, 6, 6, 5, 0, 7, 2, 2, 1, 'app.conf', 0, 1, 1, 'b', 2)`,
	}
	for _, stmt := range stmts {
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("seeding value rows: %v", err)
		}
	}
}

func insertDeploymentBlob(t *testing.T, q *Queries, id int64, value []byte) {
	t.Helper()
	insertDeploymentVersion(t, q, id, id, 1, value)
}

func insertDeploymentVersion(t *testing.T, q *Queries, id, deploymentID, version int64, value []byte) {
	t.Helper()
	if _, err := q.sqlDB().Exec(`INSERT INTO deployment_event_log (id, global_seq, event_time, created_time, author, deployment_id, version, spec_version, space_assignment_version, name_version, value, event_type)
		VALUES (?, ?, 1, 1, 0, ?, ?, ?, 1, 1, ?, 1)`, id, 100+id, deploymentID, version, version, value); err != nil {
		t.Fatalf("inserting deployment row %d: %v", id, err)
	}
}

func legacyDeploymentBlob(secretRow, configRow, assetEnvRow, assetMountRow, certRow uint64) []byte {
	envEntry := func(key string, value []byte) []byte {
		return wireMessage(2, wireStringField(1, key), appendBytesField(nil, 2, value))
	}
	runtime := wireMessage(2,
		envEntry("LIT", wireStringField(3, "x")),
		envEntry("SEC", wireVarintField(1, secretRow)),
		envEntry("CONF", wireVarintField(2, configRow)),
		envEntry("FILE", append(wireVarintField(5, assetEnvRow), wireStringField(4, "app.conf")...)),
		wireMessage(8, wireVarintField(1, assetMountRow), wireStringField(2, "/etc/app.conf")),
	)
	container := wireMessage(2, runtime, wireStringField(3, "v1"))
	certSource := wireMessage(7, wireMessage(2, wireVarintField(1, certRow)))
	https := wireMessage(4, wireVarintField(1, 8080), certSource)
	networking := wireMessage(1, wireMessage(3, wireStringField(2, "web.example.test"), https))
	spec := wireMessage(8, networking, container)
	return append(spec, wireStringField(11, "app")...)
}

func legacySystemConfigBlob() []byte {
	repo := wireMessage(4, wireMessage(1, wireVarintField(3, 11)))
	backup := wireMessage(5,
		wireMessage(1, wireVarintField(1, 1), wireMessage(2, wireVarintField(3, 21))),
		wireMessage(3, wireVarintField(3, 13)),
		wireMessage(4, wireStringField(1, "bucket")),
	)
	return append(wireMessage(1, repo, backup), wireStringField(2, "hash")...)
}

func readBlob(t *testing.T, q *Queries, table, column string, id int64) []byte {
	t.Helper()
	var b []byte
	if err := q.sqlDB().QueryRow(fmt.Sprintf(`SELECT %s FROM %s WHERE id = ?`, column, table), id).Scan(&b); err != nil {
		t.Fatalf("reading %s row %d: %v", table, id, err)
	}
	return b
}

func TestMigrateValueRefsRewritesEveryHolder(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	q := Open(path)
	seedValueRows(t, q)
	insertDeploymentBlob(t, q, 1, legacyDeploymentBlob(13, 21, 32, 31, 13))
	plain := (&apigen.Deployment{Name: "plain", SpaceID: 1}).Encode()
	insertDeploymentBlob(t, q, 2, plain)
	if _, err := q.sqlDB().Exec(`INSERT INTO system_config_revisions (id, updated_at, config_blob) VALUES (1, 1, ?)`, legacySystemConfigBlob()); err != nil {
		t.Fatalf("inserting system config: %v", err)
	}
	q.Close()

	q = Open(path)
	deployment, err := apigen.DecodeDeployment(readBlob(t, q, "deployment_event_log", "value", 1))
	if err != nil {
		t.Fatalf("decoding migrated deployment: %v", err)
	}
	if deployment.Name != "app" || deployment.Spec.Container1Spec == nil || deployment.Spec.Container1Spec.Version != "v1" {
		t.Fatalf("untouched fields lost: %+v", deployment)
	}
	env := deployment.Spec.Container1Spec.Runtime.EnvVars
	wantRef := func(field string, got *apigen.ValueRef, want apigen.ValueRef) {
		t.Helper()
		if got == nil || *got != want {
			t.Fatalf("%s = %v, want %v", field, got, want)
		}
	}
	wantRef("SEC", env["SEC"].Secret, apigen.ValueRef{ID: 3, Version: 2})
	wantRef("CONF", env["CONF"].Config, apigen.ValueRef{ID: 5, Version: 1})
	wantRef("FILE", env["FILE"].AssetRef, apigen.ValueRef{ID: 7, Version: 2})
	if env["FILE"].Asset != "app.conf" || env["LIT"].Value == nil || *env["LIT"].Value != "x" {
		t.Fatalf("env var side fields lost: FILE=%+v LIT=%+v", env["FILE"], env["LIT"])
	}
	mounts := deployment.Spec.Container1Spec.Runtime.AssetMounts
	if len(mounts) != 1 || mounts[0].Asset != (apigen.ValueRef{ID: 7, Version: 1}) || mounts[0].ContainerPath != "/etc/app.conf" {
		t.Fatalf("asset mounts = %+v", mounts)
	}
	ingress := deployment.Spec.Networking.Ingress
	if len(ingress) != 1 || ingress[0].Hostname != "web.example.test" || ingress[0].HttpsConfig.ContainerPort != 8080 ||
		ingress[0].HttpsConfig.CertSource.Secret.Secret != (apigen.ValueRef{ID: 3, Version: 2}) {
		t.Fatalf("ingress = %+v", ingress)
	}
	if got := readBlob(t, q, "deployment_event_log", "value", 2); !bytes.Equal(got, plain) {
		t.Fatalf("deployment without references was rewritten")
	}

	cfg, err := apigen.DecodeSystemConfig(readBlob(t, q, "system_config_revisions", "config_blob", 1))
	if err != nil {
		t.Fatalf("decoding migrated system config: %v", err)
	}
	settings := cfg.Settings
	if settings.Repo.GithubToken.Ref != (apigen.ValueRef{ID: 3, Version: 1}) ||
		settings.Backup.S3SecretAccessKey.Ref != (apigen.ValueRef{ID: 3, Version: 2}) ||
		settings.Backup.Enabled.ConfigRef.Ref != (apigen.ValueRef{ID: 5, Version: 1}) ||
		!settings.Backup.Enabled.Value || settings.Backup.S3Bucket.Value != "bucket" || cfg.MasterPasswordHash != "hash" {
		t.Fatalf("migrated settings = %+v", cfg)
	}

	deploymentBlob := readBlob(t, q, "deployment_event_log", "value", 1)
	configBlob := readBlob(t, q, "system_config_revisions", "config_blob", 1)
	q.Close()
	q = Open(path)
	defer q.Close()
	if !bytes.Equal(readBlob(t, q, "deployment_event_log", "value", 1), deploymentBlob) ||
		!bytes.Equal(readBlob(t, q, "system_config_revisions", "config_blob", 1), configBlob) {
		t.Fatal("second open rewrote already migrated rows")
	}
}

func TestMigrateValueRefsPanicsOnDanglingRowID(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	q := Open(path)
	seedValueRows(t, q)
	insertDeploymentBlob(t, q, 1, legacyDeploymentBlob(12, 21, 32, 31, 13))
	q.Close()

	defer func() {
		r := recover()
		msg := fmt.Sprint(r)
		if r == nil || !strings.Contains(msg, "deployment_event_log row 1") || !strings.Contains(msg, "env var secret") || !strings.Contains(msg, "row id 12") {
			t.Fatalf("recover() = %v, want a panic naming the table, row, and field", r)
		}
	}()
	Open(path)
}

func pinDeploymentVersion(t *testing.T, q *Queries, instanceID, deploymentID, version int64, state int) {
	t.Helper()
	if _, err := q.sqlDB().Exec(`INSERT INTO scheduled_instance_event_log (global_seq, event_time, created_time, scheduled_instance_id, version, deployment_id, deployment_version, deployment_spec_version, node_id, instance_ordinal, space_id, state)
		VALUES (1, 1, 1, ?, 1, ?, ?, ?, 1, 0, 1, ?)`, instanceID, deploymentID, version, version, state); err != nil {
		t.Fatalf("pinning deployment %d v%d: %v", deploymentID, version, err)
	}
}

func TestMigrateValueRefsReplacesDanglingEnvRefsInHistoricalVersions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	q := Open(path)
	seedValueRows(t, q)
	insertDeploymentVersion(t, q, 1, 9, 1, legacyDeploymentBlob(12, 99, 98, 31, 13))
	insertDeploymentVersion(t, q, 2, 9, 2, legacyDeploymentBlob(13, 21, 32, 31, 13))
	insertDeploymentVersion(t, q, 3, 9, 3, legacyDeploymentBlob(13, 21, 32, 31, 13))
	pinDeploymentVersion(t, q, 1, 9, 1, 2)
	pinDeploymentVersion(t, q, 2, 9, 2, 0)
	q.Close()

	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	q = Open(path)
	slog.SetDefault(previous)
	defer q.Close()
	for _, field := range []string{"env var secret row id 12", "env var config row id 99", "env var asset row id 98"} {
		if !strings.Contains(logs.String(), field) {
			t.Fatalf("no warning for %s in:\n%s", field, logs.String())
		}
	}
	if n := strings.Count(logs.String(), "level=WARN"); n != 3 {
		t.Fatalf("warnings = %d, want 3:\n%s", n, logs.String())
	}
	old, err := apigen.DecodeDeployment(readBlob(t, q, "deployment_event_log", "value", 1))
	if err != nil {
		t.Fatalf("decoding historical version: %v", err)
	}
	env := old.Spec.Container1Spec.Runtime.EnvVars
	for _, key := range []string{"SEC", "CONF", "FILE"} {
		v := env[key]
		if v.Value == nil || *v.Value != unknownValueRef || v.Secret != nil || v.Config != nil || v.AssetRef != nil {
			t.Fatalf("%s = %+v, want the %q literal only", key, v, unknownValueRef)
		}
	}
	if env["FILE"].Asset != "app.conf" {
		t.Fatalf("asset display key lost: %+v", env["FILE"])
	}
	if old.Spec.Container1Spec.Runtime.AssetMounts[0].Asset != (apigen.ValueRef{ID: 7, Version: 1}) {
		t.Fatalf("resolvable asset mount in the same row was not migrated: %+v", old.Spec.Container1Spec.Runtime.AssetMounts)
	}
	for _, id := range []int64{2, 3} {
		d, err := apigen.DecodeDeployment(readBlob(t, q, "deployment_event_log", "value", id))
		if err != nil {
			t.Fatalf("decoding row %d: %v", id, err)
		}
		if s := d.Spec.Container1Spec.Runtime.EnvVars["SEC"].Secret; s == nil || *s != (apigen.ValueRef{ID: 3, Version: 2}) {
			t.Fatalf("row %d SEC = %v", id, s)
		}
	}
}

func TestMigrateValueRefsPanicsOnDanglingRefInCurrentVersions(t *testing.T) {
	cases := []struct {
		name  string
		seed  func(*testing.T, *Queries)
		field string
	}{
		{"latest version", func(t *testing.T, q *Queries) {
			insertDeploymentVersion(t, q, 1, 9, 1, legacyDeploymentBlob(13, 21, 32, 31, 13))
			insertDeploymentVersion(t, q, 2, 9, 2, legacyDeploymentBlob(12, 21, 32, 31, 13))
		}, "env var secret"},
		{"pinned by a live instance", func(t *testing.T, q *Queries) {
			insertDeploymentVersion(t, q, 1, 9, 1, legacyDeploymentBlob(13, 99, 32, 31, 13))
			insertDeploymentVersion(t, q, 2, 9, 2, legacyDeploymentBlob(13, 21, 32, 31, 13))
			pinDeploymentVersion(t, q, 1, 9, 1, 4)
		}, "env var config"},
		{"historical asset mount", func(t *testing.T, q *Queries) {
			insertDeploymentVersion(t, q, 1, 9, 1, legacyDeploymentBlob(13, 21, 32, 98, 13))
			insertDeploymentVersion(t, q, 2, 9, 2, legacyDeploymentBlob(13, 21, 32, 31, 13))
		}, "asset mount"},
		{"historical ingress cert", func(t *testing.T, q *Queries) {
			insertDeploymentVersion(t, q, 1, 9, 1, legacyDeploymentBlob(13, 21, 32, 31, 12))
			insertDeploymentVersion(t, q, 2, 9, 2, legacyDeploymentBlob(13, 21, 32, 31, 13))
		}, "ingress cert secret"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "primary.db")
			q := Open(path)
			seedValueRows(t, q)
			tc.seed(t, q)
			q.Close()
			defer func() {
				if r := recover(); r == nil || !strings.Contains(fmt.Sprint(r), tc.field) {
					t.Fatalf("recover() = %v, want a panic naming %q", r, tc.field)
				}
			}()
			Open(path)
		})
	}
}
