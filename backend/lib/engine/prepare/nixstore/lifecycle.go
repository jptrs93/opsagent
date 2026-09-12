package nixstore

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"syscall"
	"time"

	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

// Size sums the store's files, counting each inode once.
func (m *Manager) Size(key string) (int64, error) {
	seen := map[[2]uint64]struct{}{}
	var total int64
	err := filepath.WalkDir(m.StoreRoot(key), func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			if errors.Is(err, fs.ErrPermission) || errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		if !d.Type().IsRegular() {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if st, ok := info.Sys().(*syscall.Stat_t); ok {
			id := [2]uint64{uint64(st.Dev), uint64(st.Ino)}
			if _, dup := seen[id]; dup {
				return nil
			}
			seen[id] = struct{}{}
		}
		total += info.Size()
		return nil
	})
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	return total, err
}

// gcTarget is how many bytes a collection must free to bring size under the
// cap with headroom, or zero when the store is within budget.
func gcTarget(size, cap int64) int64 {
	if size <= cap {
		return 0
	}
	return size - cap + cap/10
}

// AfterBuild records the store size and collects garbage when the store has
// grown past the cap. The caller holds the repository lock.
func (m *Manager) AfterBuild(ctx context.Context, store Store, out io.Writer, log Logger) {
	size, err := m.Size(store.Key)
	if err != nil {
		log("measuring store size: %v", err)
		return
	}
	log("store size %s (%d paths)", formatBytes(size), m.pathCount(store.Key))
	if target := gcTarget(size, m.cfg.SizeCap); target > 0 {
		log("store exceeds %s; collecting at least %s", formatBytes(m.cfg.SizeCap), formatBytes(target))
		started := time.Now()
		if err := m.collect(ctx, store.Key, target, out); err != nil {
			log("store garbage collection failed: %v", err)
		} else if after, err := m.Size(store.Key); err == nil {
			log("store garbage collection freed %s in %s", formatBytes(size-after), time.Since(started).Round(time.Millisecond))
			size = after
		}
	}
	meta := store.Meta
	meta.LastBuildAt = time.Now().UTC()
	meta.LastSize = size
	if err := m.writeMeta(store.Key, meta); err != nil {
		log("updating store metadata: %v", err)
	}
}

func (m *Manager) pathCount(key string) int {
	entries, err := os.ReadDir(filepath.Join(m.StoreRoot(key), "store"))
	if err != nil {
		return 0
	}
	return len(entries)
}

func (m *Manager) collect(ctx context.Context, key string, target int64, out io.Writer) error {
	spec := m.maintenanceSpec("gc", "nix", "store", "gc", "--max-freed", fmt.Sprint(target))
	spec.Mounts = []ctrd.Mount{
		{Source: m.StoreRoot(key), Dest: containerNixDir},
		{Source: m.NixConfPath(), Dest: containerNixConfPath, ReadOnly: true},
	}
	ctx, cancel := context.WithTimeout(ctx, MaintenanceTimeout)
	defer cancel()
	if out == nil {
		out = io.Discard
	}
	result, err := m.runner.RunBuild(ctx, spec, out, out)
	if err != nil {
		return err
	}
	if result.Code != 0 {
		return fmt.Errorf("nix store gc exited with status %d", result.Code)
	}
	return nil
}

// Reset deletes the store so the next build reseeds it. The caller holds the
// repository lock.
func (m *Manager) Reset(ctx context.Context, key string, out io.Writer) error {
	if !m.storeSeeded(key) {
		return nil
	}
	return m.deleteStore(ctx, key, out)
}

// RequestReset marks a repository's store for reset at the next opportunity:
// the maintenance loop when the store is idle, otherwise before its next
// build. Requests older than the store's seed time are already satisfied.
func (m *Manager) RequestReset(repoURL string, at time.Time) {
	key := Key(repoURL)
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.pendingResets[key]; ok && !at.After(current) {
		return
	}
	m.pendingResets[key] = at
}

func (m *Manager) resetPending(key string, meta Meta) bool {
	_, ok := m.pendingReset(key, meta)
	return ok
}

// pendingReset returns the outstanding reset request for a store, if one was
// made after the store was seeded.
func (m *Manager) pendingReset(key string, meta Meta) (time.Time, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	at, ok := m.pendingResets[key]
	if !ok || !meta.SeededAt.Before(at) {
		return time.Time{}, false
	}
	return at, true
}

// clearPendingReset drops a request once the reset it asked for has run; a
// newer request made meanwhile is kept.
func (m *Manager) clearPendingReset(key string, at time.Time) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if current, ok := m.pendingResets[key]; ok && current.Equal(at) {
		delete(m.pendingResets, key)
	}
}

// needsScheduledReset reports whether a store has outlived the reset interval.
func needsScheduledReset(meta Meta, interval time.Duration, now time.Time) bool {
	return !meta.SeededAt.IsZero() && now.Sub(meta.SeededAt) >= interval
}

func (m *Manager) storeKeys() []string {
	entries, err := os.ReadDir(filepath.Join(m.cfg.Root, "stores"))
	if err != nil {
		return nil
	}
	keys := make([]string, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			keys = append(keys, e.Name())
		}
	}
	return keys
}

const maintenanceInterval = 10 * time.Minute

// RunMaintenance sweeps stale scratch directories left by a crashed agent,
// then periodically resets stores that are due or requested, taking only
// idle repository locks.
func (m *Manager) RunMaintenance(ctx context.Context) {
	network.Default.CleanupContainerNets(network.BuildDeploymentID, nil)
	m.sweepScratch()
	ticker := time.NewTicker(maintenanceInterval)
	defer ticker.Stop()
	for {
		m.maintainOnce(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) sweepScratch() {
	for _, key := range m.storeKeys() {
		entries, err := os.ReadDir(m.scratchParent(key))
		if err != nil {
			continue
		}
		for _, e := range entries {
			path := filepath.Join(m.scratchParent(key), e.Name())
			if err := os.RemoveAll(path); err != nil {
				slog.WarnContext(m.ctx, "removing stale build scratch directory failed", "err", err)
			}
		}
	}
}

func (m *Manager) maintainOnce(ctx context.Context) {
	now := time.Now()
	for _, key := range m.storeKeys() {
		meta, err := m.readMeta(key)
		if err != nil {
			continue
		}
		reason := ""
		requestedAt, requested := m.pendingReset(key, meta)
		switch {
		case requested:
			reason = "operator request"
		case needsScheduledReset(meta, m.cfg.ResetInterval, now):
			reason = fmt.Sprintf("seeded %s ago", now.Sub(meta.SeededAt).Round(time.Minute))
		default:
			continue
		}
		release, ok := m.TryLock(key)
		if !ok {
			continue
		}
		slog.InfoContext(m.ctx, fmt.Sprintf("resetting nix store for %s: %s", meta.Repo, reason))
		if err := m.Reset(ctx, key, nil); err != nil {
			slog.WarnContext(m.ctx, fmt.Sprintf("resetting nix store for %s failed", meta.Repo), "err", err)
		} else if requested {
			m.clearPendingReset(key, requestedAt)
		}
		release()
	}
}

func formatBytes(size int64) string {
	const unit = 1024
	if size < unit {
		return fmt.Sprintf("%d B", size)
	}
	units := []string{"KiB", "MiB", "GiB", "TiB"}
	value := float64(size)
	for _, suffix := range units {
		value /= unit
		if value < unit || suffix == units[len(units)-1] {
			return fmt.Sprintf("%.1f %s", value, suffix)
		}
	}
	return fmt.Sprintf("%d B", size)
}
