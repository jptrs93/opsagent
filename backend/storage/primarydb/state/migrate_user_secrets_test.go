package state

import (
	"context"
	"path/filepath"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
)

func TestMigrateSecretsToEventLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "primary.db")
	q := pq.Open(path)
	ctx := context.Background()
	now := time.Now().UnixMilli()

	sec, err := q.InsertSecretRow(ctx, pq.InsertSecretRowParams{Name: "db-password", SpaceID: 1, ValueDirectoryID: 0, CreatedAt: now, CreatedBy: 7})
	if err != nil {
		t.Fatal(err)
	}
	var rowIDs []int64
	for i, ct := range []byte{1, 2, 3} {
		v, err := q.InsertSecretVersion(ctx, pq.InsertSecretVersionParams{
			SecretID: sec.ID, Version: int64(i + 1), SmkVersion: 1,
			Ciphertext: []byte{ct}, Nonce: []byte{ct, ct},
			CreatedAt: now + int64(i), CreatedBy: 7,
		})
		if err != nil {
			t.Fatal(err)
		}
		rowIDs = append(rowIDs, v.ID)
	}

	pinnedID := int32(rowIDs[0])
	spec := nonEmptySpec()
	spec.Container1Spec.Runtime.EnvVars = map[string]*apigen.EnvVarValue{
		"DB_PASSWORD": {SecretRefID: &pinnedID},
	}
	spec.Networking.Ingress = []*apigen.Ingress{{
		Kind: apigen.IngressKind_INGRESS_KIND_HTTPS,
		HttpsConfig: &apigen.HttpsConfig{
			CertSource: &apigen.CertSource{Secret: &apigen.SecretCertSource{SecretRefID: pinnedID}},
		},
	}}
	if _, err := q.CreateDeploymentConfig(ctx, pq.CreateDeploymentConfigParams{
		NodeID: 1, SpaceID: 1, Name: "app", CreatedAt: now, Version: 1, UpdatedAt: now, UpdatedBy: 0, SpecBlob: spec.Encode(), Deleted: 0,
	}); err != nil {
		t.Fatal(err)
	}
	if err := q.Close(); err != nil {
		t.Fatal(err)
	}

	s := Open(path)
	defer s.Close()

	meta, ok := s.GetSecretMeta(int32(sec.ID))
	if !ok {
		t.Fatal("secret missing after migration")
	}
	if len(meta.VersionRefs) != 2 {
		t.Fatalf("version refs = %d, want 2 (latest + pinned)", len(meta.VersionRefs))
	}

	records := s.ListSecretVersionRecords()
	if len(records) != 2 {
		t.Fatalf("records = %d", len(records))
	}
	byLegacy := map[int32][]byte{}
	for _, r := range records {
		if r.SecretID != int32(sec.ID) || r.Name != "db-password" {
			t.Fatalf("record = %+v", r)
		}
		byLegacy[r.LegacyVersion] = r.Ciphertext
	}
	if string(byLegacy[1]) != string([]byte{1}) || string(byLegacy[3]) != string([]byte{3}) {
		t.Fatalf("imported ciphertexts = %+v", byLegacy)
	}

	rows, err := s.q.ListAllDeploymentConfigs(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if rows[0].Version != 1 {
		t.Fatalf("deployment version bumped to %d", rows[0].Version)
	}
	migrated, err := apigen.DecodeDeploymentSpec(rows[0].SpecBlob)
	if err != nil {
		t.Fatal(err)
	}
	env := migrated.Container().Runtime.EnvVars["DB_PASSWORD"]
	if env.SecretRefID == nil || int64(*env.SecretRefID) <= eventIDFloor {
		t.Fatalf("env secret ref = %v", env.SecretRefID)
	}
	certRef := migrated.Networking.Ingress[0].HttpsConfig.CertSource.Secret.SecretRefID
	if certRef != *env.SecretRefID {
		t.Fatalf("cert ref = %d, env ref = %d", certRef, *env.SecretRefID)
	}
	found := false
	for _, r := range records {
		if r.ID == *env.SecretRefID && r.LegacyVersion == 1 {
			found = true
		}
	}
	if !found {
		t.Fatalf("pinned ref %d does not resolve to legacy version 1", *env.SecretRefID)
	}

	if _, done := s.FetchLocalKV(secretsEventLogMarker); !done {
		t.Fatal("marker not set")
	}
}
