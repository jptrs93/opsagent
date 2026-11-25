package logmanager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/jptrs93/goutil/contextu"
	"github.com/jptrs93/opsagent/backend/storage/logdb"
)

type SealedFile struct {
	DeploymentID int32
	Day          uint32
	Level        uint32
	Seq          int64
	MinTime      int64
	MaxTime      int64
	Rows         int64
	SizeBytes    int64
	Sha256       []byte
	Path         string
}

// sealNext must stay on the maintenance goroutine: a roll-up running beside
// it could take the file being hashed as an input.
func (m *Manager) sealNext(ctx context.Context) (bool, error) {
	files, err := m.db.ListSealableLogFiles(ctx, logdb.ListSealableLogFilesParams{
		BeforeDay: clock().Add(-sealDayEndDelay).Unix() / daySeconds,
		MaxFiles:  int64(sealCandidates + len(m.sealSkip)),
	})
	if err != nil {
		return false, err
	}
	for _, f := range files {
		if m.sealSkip[f.ID] {
			continue
		}
		started := time.Now()
		sum, err := hashFile(archiveFilePath(int32(f.DeploymentID), f), f.ByteSize)
		if err != nil {
			m.sealSkip[f.ID] = true
			slog.WarnContext(m.ctx, "sealing log file failed", "err", err, "dep", f.DeploymentID, "file", logFileName(f))
			return true, nil
		}
		if _, err := m.db.SetLogFileSha256(ctx, logdb.SetLogFileSha256Params{Sha256: hex.EncodeToString(sum), ID: f.ID}); err != nil {
			return false, err
		}
		contextu.Sleep(ctx, min(time.Since(started), sealPauseLimit))
		return true, nil
	}
	return false, nil
}

func hashFile(path string, wantSize int64) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return nil, err
	}
	if n != wantSize {
		return nil, fmt.Errorf("file is %d bytes, the catalog says %d", n, wantSize)
	}
	return h.Sum(nil), nil
}

func (m *Manager) SealedFiles(ctx context.Context, nodeID int32) ([]SealedFile, error) {
	rows, err := m.db.ListSealedLogFiles(ctx, int64(nodeID))
	if err != nil {
		return nil, err
	}
	out := make([]SealedFile, 0, len(rows))
	for _, r := range rows {
		sum, err := hex.DecodeString(r.Sha256)
		if err != nil || len(sum) != sha256.Size {
			continue
		}
		out = append(out, SealedFile{
			DeploymentID: int32(r.DeploymentID), Day: uint32(r.Day), Level: uint32(r.Level), Seq: r.Seq,
			MinTime: r.MinTime, MaxTime: r.MaxTime, Rows: r.RowCount, SizeBytes: r.ByteSize, Sha256: sum,
			Path: archiveFilePath(int32(r.DeploymentID), r),
		})
	}
	return out, nil
}
