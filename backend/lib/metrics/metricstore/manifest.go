package metricstore

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"
)

type manifest struct {
	NodeID    int32          `json:"node_id"`
	Seq       int            `json:"seq"`
	WALSha256 string         `json:"wal_sha256,omitempty"`
	Files     []manifestFile `json:"files"`
}

type manifestFile struct {
	Name         string `json:"name"`
	DeploymentID int32  `json:"deployment_id,omitempty"`
	Rows         int64  `json:"rows"`
	MinTime      int64  `json:"min_time"`
	MaxTime      int64  `json:"max_time"`
	SizeBytes    int64  `json:"size_bytes"`
	Sha256       string `json:"sha256"`
}

func readManifest(path string) (manifest, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return manifest{}, err
	}
	var m manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return manifest{}, err
	}
	return m, nil
}

// writeManifest is the commit point of a compaction: once the manifest has
// its final name, the files it lists are sealed whether or not they have
// been renamed yet.
func writeManifest(dd string, m manifest) error {
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	final := filepath.Join(dd, manifestName(m.NodeID, m.Seq))
	tmp := final + tmpExt
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return err
	}
	if _, err := f.Write(b); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, final); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return syncDir(dd)
}

type SealedFile struct {
	DeploymentID int32
	Day          uint32
	Seq          int
	MinTime      int64
	MaxTime      int64
	Rows         int64
	SizeBytes    int64
	Sha256       []byte
	Path         string
}

func SealedFiles(dir string, nodeID int32) ([]SealedFile, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []SealedFile
	for _, e := range entries {
		day, ok := parseDay(e.Name())
		if !ok || !e.IsDir() {
			continue
		}
		dd := filepath.Join(dir, e.Name())
		files, err := os.ReadDir(dd)
		if err != nil {
			return nil, err
		}
		for _, f := range files {
			n, _, ok := parseManifestName(f.Name())
			if !ok || n != nodeID {
				continue
			}
			m, err := readManifest(filepath.Join(dd, f.Name()))
			if err != nil {
				continue
			}
			for _, mf := range m.Files {
				sum, err := hex.DecodeString(mf.Sha256)
				if err != nil || len(sum) != sha256.Size {
					continue
				}
				path := filepath.Join(dd, mf.Name)
				st, err := os.Stat(path)
				if err != nil || st.Size() != mf.SizeBytes {
					continue
				}
				out = append(out, SealedFile{DeploymentID: mf.DeploymentID, Day: DayNumber(day), Seq: m.Seq, MinTime: mf.MinTime, MaxTime: mf.MaxTime,
					Rows: mf.Rows, SizeBytes: mf.SizeBytes, Sha256: sum, Path: path})
			}
		}
	}
	slices.SortFunc(out, func(a, b SealedFile) int {
		if a.Day != b.Day {
			return int(a.Day) - int(b.Day)
		}
		if a.Seq != b.Seq {
			return a.Seq - b.Seq
		}
		return int(a.DeploymentID) - int(b.DeploymentID)
	})
	return out, nil
}

func dayOfNumber(day uint32) time.Time {
	return time.Unix(int64(day)*daySeconds, 0).UTC()
}

func RunFilePath(metricsDir string, day uint32, deploymentID, nodeID int32, seq int) string {
	return filepath.Join(dayDir(metricsDir, dayOfNumber(day)), splitParquetName(deploymentID, nodeID, seq))
}

func NodeFilePath(metricsDir string, day uint32, nodeID int32, seq int) string {
	return filepath.Join(dayDir(NodeDir(metricsDir), dayOfNumber(day)), parquetName(nodeID, seq))
}

func backfillManifests[S any, R any](ctx context.Context, t *table[S, R], dir string, nodeID int32) error {
	if t.split != nil {
		return nil
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	var firstErr error
	for _, e := range entries {
		if _, ok := parseDay(e.Name()); !ok || !e.IsDir() {
			continue
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dd := filepath.Join(dir, e.Name())
		files, err := os.ReadDir(dd)
		if err != nil {
			return err
		}
		described := map[int]bool{}
		for _, f := range files {
			if n, seq, ok := parseManifestName(f.Name()); ok && n == nodeID {
				described[seq] = true
			}
		}
		for _, f := range files {
			n, ok := parseParquetName(f.Name())
			if !ok || n.split || n.nodeID != nodeID || described[n.seq] {
				continue
			}
			mf, err := describeSealed(ctx, t, dd, f.Name())
			if err == nil {
				err = writeManifest(dd, manifest{NodeID: nodeID, Seq: n.seq, Files: []manifestFile{mf}})
			}
			if err != nil {
				slog.WarnContext(ctx, fmt.Sprintf("describing sealed %s file %s failed", t.name, f.Name()), "err", err)
				if firstErr == nil {
					firstErr = err
				}
			}
		}
	}
	return firstErr
}

func describeSealed[S any, R any](ctx context.Context, t *table[S, R], dd, name string) (manifestFile, error) {
	path := filepath.Join(dd, name)
	out := manifestFile{Name: name, MinTime: math.MaxInt64, MaxTime: math.MinInt64}
	_, err := scanParquet(ctx, t, path, scanSpec[S]{}, func(*S) bool { return true }, func(s *S) bool {
		ts := t.timeOf(s)
		out.MinTime, out.MaxTime = min(out.MinTime, ts), max(out.MaxTime, ts)
		out.Rows++
		return true
	})
	if err != nil {
		return manifestFile{}, err
	}
	if out.Rows == 0 {
		return manifestFile{}, errors.New("file has no rows")
	}
	f, err := os.Open(path)
	if err != nil {
		return manifestFile{}, err
	}
	defer f.Close()
	h := sha256.New()
	if out.SizeBytes, err = io.Copy(h, f); err != nil {
		return manifestFile{}, err
	}
	out.Sha256 = hex.EncodeToString(h.Sum(nil))
	return out, nil
}
