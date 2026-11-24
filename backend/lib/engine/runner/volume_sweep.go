package runner

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/storage"
)

const volumeDeletingSuffix = ".deleting"

var volumeSweepInterval = 5 * time.Minute

type VolumeSweeper struct {
	root   string
	placed func() map[uint64]struct{}
	wake   chan struct{}

	mu    sync.Mutex
	live  map[uint64]struct{}
	known bool
}

func NewVolumeSweeper(store storage.OperatorStore, predicate storage.ScheduledInstancePredicate) *VolumeSweeper {
	return newVolumeSweeper(ainit.StaticConfig.VolumesDir, func() map[uint64]struct{} {
		states, _, unsubscribe := store.MustFetchScheduledSnapshotAndSubscribe(predicate)
		unsubscribe()
		placed := make(map[uint64]struct{}, len(states))
		for i := range states {
			placed[states[i].Instance.Deployment.DeploymentID] = struct{}{}
		}
		return placed
	})
}

func newVolumeSweeper(root string, placed func() map[uint64]struct{}) *VolumeSweeper {
	return &VolumeSweeper{root: root, placed: placed, wake: make(chan struct{}, 1)}
}

// SetLive must be called before the node learns of any instance of a
// deployment the set adds; the sweep relies on that order.
func (s *VolumeSweeper) SetLive(ids []uint64) {
	live := make(map[uint64]struct{}, len(ids))
	for _, id := range ids {
		live[id] = struct{}{}
	}
	s.mu.Lock()
	s.live, s.known = live, true
	s.mu.Unlock()
	s.Kick()
}

func (s *VolumeSweeper) Kick() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *VolumeSweeper) Run(ctx context.Context) {
	ctx = logu.AddTag(ctx, "VolumeSweep")
	ticker := time.NewTicker(volumeSweepInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.wake:
		case <-ticker.C:
		}
		s.sweep(ctx)
	}
}

func (s *VolumeSweeper) sweep(ctx context.Context) {
	// The listing must be read before the live set. A volume directory only
	// exists once its deployment is in the set, so a directory listed here and
	// missing from the set read afterwards belongs to a deleted deployment.
	entries, err := os.ReadDir(s.root)
	if err != nil {
		slog.WarnContext(ctx, "listing volume directories failed", "err", err)
		return
	}
	var candidates []uint64
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasSuffix(name, volumeDeletingSuffix) {
			s.remove(ctx, name)
			continue
		}
		if id, ok := volumeDirDeployment(name); ok && entry.IsDir() {
			candidates = append(candidates, id)
		}
	}
	s.mu.Lock()
	live, known := s.live, s.known
	s.mu.Unlock()
	if !known {
		return
	}
	var placed map[uint64]struct{}
	for _, id := range candidates {
		if _, ok := live[id]; ok {
			continue
		}
		if placed == nil {
			placed = s.placed()
		}
		if _, ok := placed[id]; ok {
			continue
		}
		name := strconv.FormatUint(id, 10)
		if err := os.Rename(filepath.Join(s.root, name), filepath.Join(s.root, name+volumeDeletingSuffix)); err != nil {
			slog.WarnContext(ctx, "marking the volumes of a deleted deployment for removal failed", "err", err, "dep", id)
			continue
		}
		s.remove(ctx, name+volumeDeletingSuffix)
	}
}

func (s *VolumeSweeper) remove(ctx context.Context, name string) {
	if err := os.RemoveAll(filepath.Join(s.root, name)); err != nil {
		slog.WarnContext(ctx, fmt.Sprintf("removing volume directory %s failed; the next sweep retries", name), "err", err)
		return
	}
	slog.InfoContext(ctx, fmt.Sprintf("removed volume directory %s of a deleted deployment", name))
}

func volumeDirDeployment(name string) (uint64, bool) {
	id, err := strconv.ParseUint(name, 10, 64)
	if err != nil || id == 0 || strconv.FormatUint(id, 10) != name {
		return 0, false
	}
	return id, true
}
