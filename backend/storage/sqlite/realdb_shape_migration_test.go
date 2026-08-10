package sqlite

// Throwaway verification against snapshots of the production databases.
// Run with:
//   OPSAGENT_REALDB_PRIMARY=/path/primary.db OPSAGENT_REALDB_SECONDARY=/path/secondary.db go test -run RealDB -v ./storage/sqlite/
// The env-var files are copied into a temp dir first; originals are never touched.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

type oldAssetRow struct {
	id        int32
	key       string
	spaceID   int32
	createdAt int64
	version   int32
	location  string
	sizeBytes int32
	blobSHA   string
}

func copyFileForTest(t *testing.T, src, dst string) {
	t.Helper()
	data, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("read %s: %v", src, err)
	}
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		t.Fatal(err)
	}
}

func readOldAssetRows(t *testing.T, path string) []oldAssetRow {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(`SELECT id, key, space_id, created_at, version, location, size_bytes, blob FROM assets ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []oldAssetRow
	for rows.Next() {
		var r oldAssetRow
		var blob []byte
		if err := rows.Scan(&r.id, &r.key, &r.spaceID, &r.createdAt, &r.version, &r.location, &r.sizeBytes, &blob); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(blob)
		r.blobSHA = hex.EncodeToString(sum[:])
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRealDBPrimaryShapeMigration(t *testing.T) {
	src := os.Getenv("OPSAGENT_REALDB_PRIMARY")
	if src == "" {
		t.Skip("OPSAGENT_REALDB_PRIMARY not set")
	}
	path := filepath.Join(t.TempDir(), "primary.db")
	copyFileForTest(t, src, path)
	old := readOldAssetRows(t, path)
	if len(old) == 0 {
		t.Fatal("no asset rows in snapshot; nothing to verify")
	}
	var maxOldID int32
	oldByKey := map[string][]oldAssetRow{}
	for _, r := range old {
		if r.id > maxOldID {
			maxOldID = r.id
		}
		oldByKey[r.key] = append(oldByKey[r.key], r)
	}
	t.Logf("snapshot: %d version rows, %d distinct keys, max row id %d", len(old), len(oldByKey), maxOldID)

	store := NewPrimaryStorage(path)

	// Every pre-migration row survives verbatim under its original id.
	for _, want := range old {
		v, ok := store.GetAssetVersionByIDIncludingPending(want.id)
		if !ok {
			t.Fatalf("version row %d (%s v%d) missing after migration", want.id, want.key, want.version)
		}
		sum := sha256.Sum256(v.Blob)
		if v.Key != want.key || v.SpaceID != want.spaceID || v.Version != want.version ||
			v.Location != want.location || v.SizeBytes != want.sizeBytes ||
			v.CreatedAt.UnixMilli() != want.createdAt || hex.EncodeToString(sum[:]) != want.blobSHA {
			t.Fatalf("version row %d changed:\n got %+v (blob sha %x)\nwant %+v", want.id, v, sum, want)
		}
	}

	// One asset per key, ids above every preserved version id, refs complete
	// and newest first.
	items := store.ListAssets()
	if len(items) != len(oldByKey) {
		t.Fatalf("asset list = %d entries, want %d", len(items), len(oldByKey))
	}
	for _, meta := range items {
		group := oldByKey[meta.Key]
		if group == nil {
			t.Fatalf("unexpected asset %q", meta.Key)
		}
		if meta.ID <= maxOldID {
			t.Fatalf("asset id %d for %q overlaps preserved version ids (max %d)", meta.ID, meta.Key, maxOldID)
		}
		if len(meta.VersionRefs) != len(group) {
			t.Fatalf("asset %q has %d version refs, want %d", meta.Key, len(meta.VersionRefs), len(group))
		}
		var latest oldAssetRow
		for _, r := range group {
			if r.version > latest.version {
				latest = r
			}
		}
		ref := meta.VersionRefs[0]
		if ref.ID != latest.id || ref.Version != latest.version || ref.SizeBytes != latest.sizeBytes ||
			ref.Location != latest.location || ref.CreatedAt.UnixMilli() != latest.createdAt {
			t.Fatalf("asset %q version_refs[0] = %+v, want row %+v", meta.Key, ref, latest)
		}
		for i := 1; i < len(meta.VersionRefs); i++ {
			if meta.VersionRefs[i].Version >= meta.VersionRefs[i-1].Version {
				t.Fatalf("asset %q refs not newest-first: %+v", meta.Key, meta.VersionRefs)
			}
		}
	}

	// Every asset version pinned by a live deployment config still resolves.
	pinned := map[int32]string{}
	collect := func(c *apigen.ContainerSpec, deployment string) {
		if c == nil {
			return
		}
		for _, m := range c.Runtime.AssetMounts {
			if m.AssetVersionID != 0 {
				pinned[m.AssetVersionID] = deployment
			}
		}
		for name, ev := range c.Runtime.EnvVars {
			if ev != nil && ev.AssetVersionID != 0 {
				pinned[ev.AssetVersionID] = deployment + " env " + name
			}
		}
	}
	for _, cfg := range store.ListActiveDeploymentConfigs() {
		name := cfg.Identity.Name
		collect(cfg.Spec.Container1Spec, name)
		collect(cfg.Spec.Container2Spec, name)
		collect(cfg.Spec.Container3Spec, name)
	}
	t.Logf("live deployment configs pin %d distinct asset versions", len(pinned))
	for id, where := range pinned {
		v, ok := store.GetAssetVersionByID(id)
		if !ok {
			t.Fatalf("asset version %d pinned by %s does not resolve after migration", id, where)
		}
		t.Logf("  pin %d -> %s v%d (%s)", id, v.Key, v.Version, where)
	}

	if err := store.Close(); err != nil {
		t.Fatal(err)
	}

	// Second startup: idempotent, nothing changes.
	store = NewPrimaryStorage(path)
	defer store.Close()
	if got := store.ListAssets(); len(got) != len(oldByKey) {
		t.Fatalf("second startup asset count = %d, want %d", len(got), len(oldByKey))
	}
	for _, want := range old {
		if _, ok := store.GetAssetVersionByIDIncludingPending(want.id); !ok {
			t.Fatalf("second startup lost version row %d", want.id)
		}
	}
}

type oldValueRow struct {
	id        int32
	name      string
	spaceID   int32
	version   int32
	createdAt int64
	updatedBy int32
	valueSHA  string // sha256 of ciphertext (secrets) or value text (configs)
}

func readOldValueRows(t *testing.T, path, table, valueColumn string) []oldValueRow {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+path)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	rows, err := db.Query(fmt.Sprintf(`SELECT id, name, space_id, version, created_at, updated_by, %s FROM %s ORDER BY id`, valueColumn, table))
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []oldValueRow
	for rows.Next() {
		var r oldValueRow
		var value []byte
		if err := rows.Scan(&r.id, &r.name, &r.spaceID, &r.version, &r.createdAt, &r.updatedBy, &value); err != nil {
			t.Fatal(err)
		}
		sum := sha256.Sum256(value)
		r.valueSHA = hex.EncodeToString(sum[:])
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestRealDBPrimaryValueShapeMigration(t *testing.T) {
	src := os.Getenv("OPSAGENT_REALDB_PRIMARY")
	if src == "" {
		t.Skip("OPSAGENT_REALDB_PRIMARY not set")
	}
	path := filepath.Join(t.TempDir(), "primary.db")
	copyFileForTest(t, src, path)
	oldSecrets := readOldValueRows(t, path, "secrets", "ciphertext")
	oldConfigs := readOldValueRows(t, path, "configs", "value")
	if len(oldSecrets) == 0 || len(oldConfigs) == 0 {
		t.Fatal("no secret/config rows in snapshot; nothing to verify")
	}
	t.Logf("snapshot: %d secret rows, %d config rows", len(oldSecrets), len(oldConfigs))

	store := NewPrimaryStorage(path)
	defer store.Close()

	// Every secret version row survives verbatim, ciphertext byte-identical:
	// the SMK is not available here, so the migration must not touch it (the
	// AAD re-seal happens in the Manager at first unlock on the real machine).
	secretRecords := map[int32]struct {
		secretID int32
		name     string
		version  int32
		sha      string
	}{}
	var maxOldSecretID int32
	for _, rec := range store.ListSecretVersionRecords() {
		sum := sha256.Sum256(rec.Ciphertext)
		secretRecords[rec.ID] = struct {
			secretID int32
			name     string
			version  int32
			sha      string
		}{rec.SecretID, rec.Name, rec.Version, hex.EncodeToString(sum[:])}
	}
	for _, want := range oldSecrets {
		if want.id > maxOldSecretID {
			maxOldSecretID = want.id
		}
		got, ok := secretRecords[want.id]
		if !ok {
			t.Fatalf("secret version %d (%s) missing after migration", want.id, want.name)
		}
		if got.name != want.name || got.version != want.version || got.sha != want.valueSHA {
			t.Fatalf("secret version %d changed: got %+v want %+v", want.id, got, want)
		}
		if got.secretID <= maxOldSecretID && got.secretID <= want.id {
			// identity ids are seeded above every preserved version id
			t.Fatalf("secret identity id %d overlaps version id space", got.secretID)
		}
	}

	// Config versions: ids and values preserved; metas group by name.
	for _, want := range oldConfigs {
		ref, ok := store.GetConfigVersionByID(want.id)
		if !ok {
			t.Fatalf("config version %d (%s) missing after migration", want.id, want.name)
		}
		sum := sha256.Sum256([]byte(ref.Value))
		if ref.Name != want.name || ref.Version != want.version || hex.EncodeToString(sum[:]) != want.valueSHA {
			t.Fatalf("config version %d changed: got %+v want %+v", want.id, ref, want)
		}
	}

	// Every env ref pinned by a live deployment config still resolves.
	secretPins, configPins := map[int32]string{}, map[int32]string{}
	collect := func(c *apigen.ContainerSpec, deployment string) {
		if c == nil {
			return
		}
		for name, ev := range c.Runtime.EnvVars {
			if ev == nil {
				continue
			}
			if ev.SecretVersionID != nil && *ev.SecretVersionID != 0 {
				secretPins[*ev.SecretVersionID] = deployment + " env " + name
			}
			if ev.ConfigVersionID != nil && *ev.ConfigVersionID != 0 {
				configPins[*ev.ConfigVersionID] = deployment + " env " + name
			}
		}
	}
	for _, cfg := range store.ListActiveDeploymentConfigs() {
		collect(cfg.Spec.Container1Spec, cfg.Identity.Name)
		collect(cfg.Spec.Container2Spec, cfg.Identity.Name)
		collect(cfg.Spec.Container3Spec, cfg.Identity.Name)
	}
	t.Logf("live deployment configs pin %d secret versions and %d config versions", len(secretPins), len(configPins))
	for id, where := range secretPins {
		got, ok := secretRecords[id]
		if !ok {
			t.Fatalf("secret version %d pinned by %s does not resolve after migration", id, where)
		}
		t.Logf("  secret pin %d -> %s v%d (%s)", id, got.name, got.version, where)
	}
	for id, where := range configPins {
		ref, ok := store.GetConfigVersionByID(id)
		if !ok {
			t.Fatalf("config version %d pinned by %s does not resolve after migration", id, where)
		}
		t.Logf("  config pin %d -> %s v%d (%s)", id, ref.Name, ref.Version, where)
	}

	// System settings refs (SecretRef/ConfigRef pin version row ids too).
	if revision, err := store.FetchLatestOpenDeployConfig(); err == nil {
		cfg, err := apigen.DecodePrimaryConfig(revision.ConfigBlob)
		if err != nil {
			t.Fatalf("decode primary config: %v", err)
		}
		settings := cfg.Settings
		for label, id := range map[string]int32{
			"https_web.tls_cert_pem":            settings.HttpsWeb.TlsCertPem.VersionID,
			"repo.github_token":                 settings.Repo.GithubToken.VersionID,
			"backup.s3_secret_access_key":       settings.Backup.S3SecretAccessKey.VersionID,
			"large_assets.s3_secret_access_key": settings.LargeAssets.S3SecretAccessKey.VersionID,
		} {
			if id == 0 {
				continue
			}
			got, ok := secretRecords[id]
			if !ok {
				t.Fatalf("settings secret ref %s -> %d does not resolve after migration", label, id)
			}
			t.Logf("  settings secret ref %s -> %s v%d", label, got.name, got.version)
		}
	}
}

func TestRealDBSecondaryShapeMigration(t *testing.T) {
	src := os.Getenv("OPSAGENT_REALDB_SECONDARY")
	if src == "" {
		t.Skip("OPSAGENT_REALDB_SECONDARY not set")
	}
	path := filepath.Join(t.TempDir(), "secondary.db")
	copyFileForTest(t, src, path)
	old := readOldAssetRows(t, path)
	t.Logf("secondary snapshot: %d asset rows", len(old))

	store := NewSecondaryStorage(path)
	defer store.Close()

	var count int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM asset_versions`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != len(old) {
		t.Fatalf("asset_versions rows = %d, want %d", count, len(old))
	}
	var hasBlob int
	if err := store.db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('assets') WHERE name = 'blob'`).Scan(&hasBlob); err != nil {
		t.Fatal(err)
	}
	if hasBlob != 0 {
		t.Fatal("assets table still has old shape after migration")
	}
}
