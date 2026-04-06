package slave

import (
	"context"
	"crypto/tls"
	"io"
	"log/slog"
	"os"
	"time"

	"path/filepath"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/cluster"
	"github.com/jptrs93/opsagent/backend/engine"
	"github.com/jptrs93/opsagent/backend/engine/preparer"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

// Config holds the configuration for a slave node.
type Config struct {
	TLS         *tls.Config
	PrimaryAddr string
	MachineName string
	DataDir     string
	GithubToken string
}

// Run boots the local store, seeds the in-memory snapshot, starts the
// deployment operator, and spawns a background goroutine that maintains a
// persistent connection to the primary. It blocks until ctx is done.
func Run(ctx context.Context, cfg Config) error {
	store := sqlite.NewStorageAdapter(filepath.Join(cfg.DataDir, "secondary.db"))

	preparer.Nix = preparer.NewNixBuilder(cfg.DataDir, cfg.GithubToken)
	preparer.GHRel = preparer.NewGithubReleaseDownloader(cfg.DataDir, cfg.GithubToken)

	go engine.DeploymentOperator{Store: store}.RunAll(ctx, cfg.MachineName)

	go runPrimaryConnLoop(ctx, cfg, store)

	<-ctx.Done()
	return ctx.Err()
}

// runPrimaryConnLoop dials the primary in a loop, running a session while
// connected and backing off on dial failures. The slave keeps operating
// off local state while disconnected — this loop never returns until ctx
// is done.
func runPrimaryConnLoop(ctx context.Context, cfg Config, store *sqlite.StorageAdapter) {
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}

		conn, err := cluster.Dial(cfg.PrimaryAddr, cfg.TLS)
		if err != nil {
			slog.Warn("primary dial failed", "addr", cfg.PrimaryAddr, "err", err)
			select {
			case <-ctx.Done():
				return
			case <-time.After(backoff):
			}
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}
		backoff = time.Second

		slog.Info("slave connected to primary", "peer", conn.PeerName())
		runSession(ctx, conn, store)
		conn.Close()
	}
}

// runSession handles one connected cluster session: read the initial
// snapshot, apply it, then process incoming config updates and log
// requests. Returns when the connection drops or ctx is done.
//
// TODO: wire outgoing status-push. Needs a pushOut hook on mem.
func runSession(ctx context.Context, conn *cluster.Conn, store *sqlite.StorageAdapter) {
	for {
		select {
		case <-ctx.Done():
			return
		default:
		}

		payload, err := conn.ReadFrame()
		if err != nil {
			slog.Info("primary connection read error", "err", err)
			return
		}
		msg, err := apigen.DecodeMsgToWorker(payload)
		if err != nil {
			slog.Warn("failed decoding message from primary", "err", err)
			continue
		}

		switch {
		case msg.DeploymentsSnapshot != nil:
			// TODO: apply snapshot to mem store.
			slog.Info("received deployments snapshot", "count", len(msg.DeploymentsSnapshot.Items))
		case msg.DeploymentUpdate != nil:
			// TODO: apply deployment update to mem store.
			slog.Info("received deployment update", "seq_no", msg.DeploymentUpdate.SeqNo)
		case msg.PrepareLogRequest != nil:
			go streamPrepareLog(conn, msg.PrepareLogRequest)
		case msg.RunLogRequest != nil:
			go streamRunLog(conn, msg.RunLogRequest)
		}
	}
}

// streamPrepareLog reads a prepare output file and sends it back to the primary
// as a series of LogData frames followed by a LogEnd frame.
func streamPrepareLog(conn *cluster.Conn, req *apigen.PrepareOutputRequest) {
	streamFile(conn, req.OutputPath())
}

// streamRunLog reads a run output file and sends it back to the primary.
func streamRunLog(conn *cluster.Conn, req *apigen.RunOutputRequest) {
	streamFile(conn, req.OutputPath())
}

// streamFile reads a file and sends its contents as LogData frames, followed
// by a LogEnd frame.
func streamFile(conn *cluster.Conn, path string) {
	f, err := os.Open(path)
	if err != nil {
		slog.Error("failed opening log file for streaming", "path", path, "err", err)
		end := &apigen.MsgToMaster{LogEnd: true}
		_ = conn.WriteFrame(end.Encode())
		return
	}
	defer f.Close()

	buf := make([]byte, 32*1024)
	for {
		n, readErr := f.Read(buf)
		if n > 0 {
			chunk := make([]byte, n)
			copy(chunk, buf[:n])
			msg := &apigen.MsgToMaster{LogData: chunk}
			if werr := conn.WriteFrame(msg.Encode()); werr != nil {
				slog.Error("failed writing log data to primary", "err", werr)
				return
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			slog.Error("failed reading log file", "path", path, "err", readErr)
			break
		}
	}

	end := &apigen.MsgToMaster{LogEnd: true}
	_ = conn.WriteFrame(end.Encode())
}
