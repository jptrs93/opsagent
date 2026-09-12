package nixstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"
)

// Meta is the agent's record of one store, kept beside it.
type Meta struct {
	Repo        string    `json:"repo"`
	ImageDigest string    `json:"image_digest"`
	SeededAt    time.Time `json:"seeded_at"`
	LastBuildAt time.Time `json:"last_build_at,omitempty"`
	LastSize    int64     `json:"last_size,omitempty"`
}

// Store is one repository's seeded Nix root.
type Store struct {
	Key  string
	Dir  string
	Root string
	Meta Meta
}

func (m *Manager) readMeta(key string) (Meta, error) {
	raw, err := os.ReadFile(m.metaPath(key))
	if err != nil {
		return Meta{}, err
	}
	var meta Meta
	if err := json.Unmarshal(raw, &meta); err != nil {
		return Meta{}, err
	}
	return meta, nil
}

func (m *Manager) writeMeta(key string, meta Meta) error {
	raw, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return err
	}
	tmp := m.metaPath(key) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, m.metaPath(key))
}

func (m *Manager) storeSeeded(key string) bool {
	info, err := os.Stat(filepath.Join(m.StoreRoot(key), "store"))
	return err == nil && info.IsDir()
}

func (m *Manager) templateSeeded(digest string) bool {
	info, err := os.Stat(filepath.Join(m.templateDir(digest), "nix", "store"))
	return err == nil && info.IsDir()
}

// Ensure returns the repository's store, seeding it from the template for the
// current build image when it does not exist, was seeded from another image,
// or has a reset outstanding. The caller holds the repository lock.
func (m *Manager) Ensure(ctx context.Context, key string, repo string, out io.Writer, log Logger) (Store, error) {
	if log == nil {
		log = func(string, ...any) {}
	}
	if err := m.ensureDirs(); err != nil {
		return Store{}, fmt.Errorf("preparing nix store root: %w", err)
	}
	digest, err := m.EnsureImage(ctx, log)
	if err != nil {
		return Store{}, err
	}
	meta, metaErr := m.readMeta(key)
	seeded := m.storeSeeded(key)
	requestedAt, requested := m.pendingReset(key, meta)
	switch {
	case seeded && metaErr != nil:
		log("store %s has no metadata; reseeding", key)
	case seeded && meta.ImageDigest != digest:
		log("store %s was seeded from build image %s; reseeding from %s", key, meta.ImageDigest, digest)
	case seeded && requested:
		log("store %s has an operator reset outstanding; reseeding", key)
	case seeded:
		return Store{Key: key, Dir: m.StoreDir(key), Root: m.StoreRoot(key), Meta: meta}, nil
	}
	if seeded {
		if err := m.deleteStore(ctx, key, out); err != nil {
			return Store{}, err
		}
	}
	if err := m.ensureTemplate(ctx, digest, out, log); err != nil {
		return Store{}, err
	}
	started := time.Now()
	log("creating store for repository %s from template %s", repo, digestHex(digest))
	if err := os.MkdirAll(m.StoreDir(key), 0o755); err != nil {
		return Store{}, err
	}
	if err := m.runMaintenance(ctx, "seed", m.seedStoreScript(key, digest), out); err != nil {
		return Store{}, fmt.Errorf("seeding store: %w", err)
	}
	if !m.storeSeeded(key) {
		return Store{}, errors.New("seeding store: store directory missing after seed")
	}
	meta = Meta{Repo: repo, ImageDigest: digest, SeededAt: time.Now().UTC()}
	if err := m.writeMeta(key, meta); err != nil {
		return Store{}, fmt.Errorf("writing store metadata: %w", err)
	}
	if requested {
		m.clearPendingReset(key, requestedAt)
	}
	log("store created in %s", time.Since(started).Round(time.Millisecond))
	return Store{Key: key, Dir: m.StoreDir(key), Root: m.StoreRoot(key), Meta: meta}, nil
}

func (m *Manager) ensureTemplate(ctx context.Context, digest string, out io.Writer, log Logger) error {
	if m.templateSeeded(digest) {
		return nil
	}
	started := time.Now()
	log("seeding store template for build image %s", digest)
	if err := m.runMaintenance(ctx, "template", m.seedTemplateScript(digest), out); err != nil {
		return fmt.Errorf("seeding template: %w", err)
	}
	if !m.templateSeeded(digest) {
		return errors.New("seeding template: template directory missing after seed")
	}
	log("template seeded in %s", time.Since(started).Round(time.Millisecond))
	return nil
}

// seedTemplateScript copies the image's /nix into the template directory for
// its digest and drops templates of other digests, which no store uses once
// every store has been reseeded.
func (m *Manager) seedTemplateScript(digest string) string {
	templates := m.containerPath(m.templatesDir())
	target := m.containerPath(m.templateDir(digest))
	tmp := target + ".tmp"
	return fmt.Sprintf(`rm -rf %[1]s
for d in %[3]s/*; do [ "$d" = %[2]s ] || rm -rf "$d"; done
mkdir -p %[1]s
cp -a %[4]s %[1]s/nix
mv %[1]s %[2]s
`, shellQuote(tmp), shellQuote(target), templates, containerNixDir)
}

// seedStoreScript hardlinks the template's store paths and copies its
// database and profiles, so each store owns its SQLite database.
func (m *Manager) seedStoreScript(key string, digest string) string {
	template := m.containerPath(filepath.Join(m.templateDir(digest), "nix"))
	target := m.containerPath(m.StoreRoot(key))
	tmp := target + ".tmp"
	return fmt.Sprintf(`rm -rf %[1]s %[2]s
mkdir -p %[1]s
cp -al %[3]s/store %[1]s/store
cp -a %[3]s/var %[1]s/var
mv %[1]s %[2]s
`, shellQuote(tmp), shellQuote(target), shellQuote(template))
}

func (m *Manager) deleteStore(ctx context.Context, key string, out io.Writer) error {
	root := m.containerPath(m.StoreRoot(key))
	scratch := m.containerPath(m.scratchParent(key))
	script := fmt.Sprintf("rm -rf %s %s %s\n", shellQuote(root), shellQuote(root+".tmp"), shellQuote(scratch))
	if err := m.runMaintenance(ctx, "delete", script, out); err != nil {
		return fmt.Errorf("deleting store: %w", err)
	}
	for _, path := range []string{m.metaPath(key), m.RecordPath(key)} {
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return nil
}
