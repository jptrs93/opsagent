package slave

import (
	"context"
	"crypto/tls"
	"fmt"
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
			slog.Warn("primary dial failed; slave is disconnected",
				"addr", cfg.PrimaryAddr,
				"retry_in", backoff,
				"err", err)
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

		connectedAt := time.Now()
		slog.Info("slave connected to primary",
			"addr", cfg.PrimaryAddr,
			"peer", conn.PeerName())
		err = runSession(ctx, conn, store, cfg.MachineName)
		if err != nil && ctx.Err() == nil {
			slog.Warn("slave disconnected from primary; reconnecting",
				"addr", cfg.PrimaryAddr,
				"peer", conn.PeerName(),
				"connected_for", time.Since(connectedAt).Round(time.Second),
				"err", err)
		}
		conn.Close()
	}
}

// runSession handles one connected cluster session: read messages from the
// primary (snapshot, config updates, log requests), apply state to the local
// store, and push local status changes back. Returns when the connection
// drops or ctx is done.
func runSession(ctx context.Context, conn *cluster.Conn, store *sqlite.StorageAdapter, machine string) error {
	sessCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Subscribe to local deployment updates to push status back to primary.
	statusCh, unsub := store.SubscribeDeploymentUpdates(machine)
	defer unsub()

	go statusPushLoop(sessCtx, conn, statusCh)

	// Read timeout: the primary sends heartbeats every 5s. If we don't
	// receive any frame within 15s, assume the connection is dead.
	const readTimeout = 15 * time.Second

	for {
		select {
		case <-sessCtx.Done():
			return sessCtx.Err()
		default:
		}

		conn.SetReadDeadline(time.Now().Add(readTimeout))
		payload, err := conn.ReadFrame()
		if err != nil {
			return err
		}
		msg, err := apigen.DecodeMsgToWorker(payload)
		if err != nil {
			slog.Warn("failed decoding message from primary", "err", err)
			continue
		}

		msgType := "heartbeat"
		switch {
		case msg.DeploymentsSnapshot != nil:
			msgType = "deployments_snapshot"
		case msg.DeploymentUpdate != nil:
			msgType = "deployment_update"
		case msg.PrepareLogRequest != nil:
			msgType = "prepare_log_request"
		case msg.RunLogRequest != nil:
			msgType = "run_log_request"
		}
		slog.Info("received message from primary", "type", msgType)

		switch {
		case msg.DeploymentsSnapshot != nil:
			applySnapshot(sessCtx, store, msg.DeploymentsSnapshot)
		case msg.DeploymentUpdate != nil:
			applyConfigUpdate(sessCtx, store, msg.DeploymentUpdate)
		case msg.PrepareLogRequest != nil:
			go streamPrepareLog(conn, msg.PrepareLogRequest)
		case msg.RunLogRequest != nil:
			go streamRunLog(conn, msg.RunLogRequest)
		}
	}
}

// statusPushLoop forwards local status changes to the primary. It tracks the
// last StatusSeqNo sent per deployment to avoid echoing back statuses that
// were just applied from a config write notification.
func statusPushLoop(ctx context.Context, conn *cluster.Conn, ch <-chan apigen.DeploymentWithStatus) {
	lastSeq := make(map[apigen.DeploymentIdentifier]int32)
	for {
		select {
		case <-ctx.Done():
			return
		case dws, ok := <-ch:
			if !ok {
				return
			}
			if dws.Status == nil || dws.Config == nil || dws.Config.ID == nil {
				continue
			}
			id := *dws.Config.ID
			if dws.Status.StatusSeqNo <= lastSeq[id] {
				continue
			}
			lastSeq[id] = dws.Status.StatusSeqNo
			msg := &apigen.MsgToMaster{StatusWrite: dws.Status}
			if err := conn.WriteFrame(msg.Encode()); err != nil {
				slog.Warn("failed sending status to primary", "err", err)
				return
			}
		}
	}
}

// applySnapshot writes each deployment config and status from the primary's
// snapshot into the local store so the operator can reconcile them.
func applySnapshot(ctx context.Context, store *sqlite.StorageAdapter, snap *apigen.DeploymentWithStatusSnapshot) {
	slog.Info("applying deployments snapshot from primary", "count", len(snap.Items))
	for _, item := range snap.Items {
		if item.Config == nil || item.Config.ID == nil {
			continue
		}
		store.MustWriteDeploymentConfig(ctx, item.Config)
		if item.Status != nil {
			store.MustWriteDeploymentStatus(ctx, *item.Config.ID, func(dst *apigen.DeploymentStatus) {
				*dst = *item.Status
			})
		}
	}
}

// applyConfigUpdate writes a single config update from the primary into the
// local store.
func applyConfigUpdate(ctx context.Context, store *sqlite.StorageAdapter, cfg *apigen.DeploymentConfig) {
	if cfg == nil || cfg.ID == nil {
		return
	}
	slog.Info("applying deployment config update from primary",
		"id", fmt.Sprintf("%s:%s:%s", cfg.ID.Environment, cfg.ID.Machine, cfg.ID.Name),
		"seqNo", cfg.SeqNo)
	store.MustWriteDeploymentConfig(ctx, cfg)
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
