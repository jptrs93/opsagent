package primary

import (
	"context"
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/jptrs93/goutil/logu"
	"github.com/jptrs93/opsagent/backend/ainit"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

// RebuildTables regenerates the materialised tables from the write log and
// exits. It is the repair path for a table that disagrees with the log; it
// opens the database as a writer, so the primary must be stopped.
func RebuildTables(ctx context.Context) error {
	ctx = logu.AddTag(ctx, "Primary")
	store := state.Open(filepath.Join(ainit.StaticConfig.DataDir, "primary.db"))
	defer store.Close()
	start := time.Now()
	err := store.Queries().Tx(ctx, func(tx *pq.Queries) error { return tx.RebuildFromLog(ctx) })
	if err != nil {
		return fmt.Errorf("rebuilding the materialised tables: %w", err)
	}
	slog.InfoContext(ctx, fmt.Sprintf("materialised tables rebuilt from the write log in %s", time.Since(start).Round(time.Millisecond)))
	return nil
}
