package secondary

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/lib/engine/ctrd"
	"github.com/jptrs93/opsagent/backend/lib/engine/prepare/runtimeinputs"
	"github.com/jptrs93/opsagent/backend/lib/machinekey"
	"github.com/jptrs93/opsagent/backend/lib/wgkey"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
	"github.com/jptrs93/opsagent/backend/util/certu"
)

var errEvicted = errors.New("this node was evicted from the cluster")

const evictedMarkerName = "evicted"
const evictionTeardownTimeout = 60 * time.Second

func evictedMarkerExists(dataDir string) bool {
	_, err := os.Stat(filepath.Join(dataDir, evictedMarkerName))
	return err == nil
}

func handleEviction(ctx context.Context, cfg runtimeConfig, store *state.Service) {
	ctx = logu.AddTag(ctx, "Eviction")
	slog.ErrorContext(ctx, "this node was evicted from the cluster; stopping workloads and wiping cached cluster data")
	marker := filepath.Join(cfg.DataDir, evictedMarkerName)
	if err := os.WriteFile(marker, []byte(time.Now().UTC().Format(time.RFC3339)+"\n"), 0o600); err != nil {
		slog.ErrorContext(ctx, "writing the eviction marker failed", "err", err)
	}
	finalized := store.MustFinalizeScheduledInstancesAbsent(map[int32]struct{}{})
	slog.InfoContext(ctx, fmt.Sprintf("finalized cached scheduled instances count=%d", len(finalized)))
	waitForContainersStopped(ctx, evictionTeardownTimeout)
	removeAllContainers(ctx)
	wipeClusterData(ctx, cfg)
	slog.ErrorContext(ctx, "eviction finished; this node will not rejoin the cluster; reinstall the secondary to enroll a new identity")
}

func waitForContainersStopped(ctx context.Context, timeout time.Duration) {
	deadline := time.Now().Add(timeout)
	for {
		states, err := ctrd.Default.ListContainerStates(ctx)
		if err != nil {
			slog.WarnContext(ctx, "listing containers during eviction failed", "err", err)
			return
		}
		running := 0
		for _, st := range states {
			if st.TaskRunning {
				running++
			}
		}
		if running == 0 {
			return
		}
		if time.Now().After(deadline) {
			slog.WarnContext(ctx, fmt.Sprintf("containers still running after %s; removing them forcibly running=%d", timeout, running))
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(2 * time.Second):
		}
	}
}

func removeAllContainers(ctx context.Context) {
	states, err := ctrd.Default.ListContainerStates(ctx)
	if err != nil {
		return
	}
	for _, st := range states {
		if err := ctrd.Default.Remove(ctx, st.ID); err != nil {
			slog.WarnContext(ctx, fmt.Sprintf("removing container %s during eviction failed", st.ID), "err", err)
			continue
		}
		slog.InfoContext(ctx, fmt.Sprintf("removed container %s", st.ID))
	}
}

func wipeClusterData(ctx context.Context, cfg runtimeConfig) {
	caPath, certPath, keyPath := certu.SecondaryTLSPaths(ainit.StaticConfig.TLSDir)
	db := filepath.Join(cfg.DataDir, "secondary.db")
	files := []string{
		db, db + "-wal", db + "-shm",
		filepath.Join(cfg.DataDir, machinekey.FileName),
		filepath.Join(cfg.DataDir, wgkey.FileName),
		caPath, certPath, keyPath,
		cfg.ClusterCertPath, cfg.ClusterKeyPath,
		cfg.NetproxyStatePath,
	}
	for _, path := range files {
		if path == "" {
			continue
		}
		if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
			slog.WarnContext(ctx, fmt.Sprintf("removing %s during eviction failed", path), "err", err)
		}
	}
	if err := os.RemoveAll(runtimeinputs.IssuedTLSRoot()); err != nil {
		slog.WarnContext(ctx, "removing issued TLS material during eviction failed", "err", err)
	}
}
