package nix2container

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"sync"

	"github.com/opencontainers/go-digest"
)

type Record struct {
	path    string
	mu      sync.Mutex
	entries map[string]string
}

func OpenRecord(path string) (*Record, error) {
	record := &Record{path: path, entries: map[string]string{}}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return record, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, &record.entries); err != nil {
		record.entries = map[string]string{}
	}
	return record, nil
}

func (r *Record) Verified(key string, dgst digest.Digest) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.entries[key] == dgst.String()
}

func (r *Record) Put(key string, dgst digest.Digest) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.entries[key] = dgst.String()
}

func (r *Record) Len() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.entries)
}

func (r *Record) Save() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	raw, err := json.MarshalIndent(r.entries, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(r.path), 0o750); err != nil {
		return err
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o640); err != nil {
		return err
	}
	return os.Rename(tmp, r.path)
}

func LayerKey(paths []Path) string {
	sorted := make([]Path, len(paths))
	copy(sorted, paths)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].Path < sorted[j].Path })
	raw, _ := json.Marshal(sorted)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
