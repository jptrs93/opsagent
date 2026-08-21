package logmanager

import (
	"errors"
	"fmt"
	"io"
	"iter"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/jptrs93/opsagent/backend/storage/logdb"
	"github.com/parquet-go/parquet-go"
	"github.com/parquet-go/parquet-go/compress/zstd"
)

const (
	archiveLevelBatch = 0
	rowGroupRows      = 128 * 1024
	writeBatchRows    = 4096
	archiveExt        = ".parquet"
	tmpExt            = ".tmp"
)

// TODO: parse JSON lines into StructuredLogLine fields and shred them into
// their own columns. Until then every line is stored verbatim in raw_message,
// which is also what unparseable lines will always fall back to.
// Node is per row, not just per file: an L0 file is node-local and repeats one
// value, but a cross-node compaction merges rows from several nodes into one
// file, where the node in the file name and in log_files can no longer describe
// the contents.
type logRow struct {
	Time            int64  `parquet:"time"`
	Version         int32  `parquet:"version"`
	Run             int32  `parquet:"run"`
	Node            int32  `parquet:"node"`
	InstanceOrdinal int32  `parquet:"instance_ordinal"`
	Stream          int32  `parquet:"stream"`
	Level           string `parquet:"level,optional"`
	Msg             string `parquet:"msg,optional"`
	RawMessage      []byte `parquet:"raw_message"`
}

type archiveWriter struct {
	file    *os.File
	writer  *parquet.GenericWriter[logRow]
	pending []logRow
	count   int64
	minTime int64
	maxTime int64
}

func newArchiveWriter(path string) (*archiveWriter, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o640)
	if err != nil {
		return nil, err
	}
	w := parquet.NewGenericWriter[logRow](f,
		parquet.Compression(&zstd.Codec{}),
		parquet.MaxRowsPerRowGroup(rowGroupRows),
	)
	return &archiveWriter{file: f, writer: w, pending: make([]logRow, 0, writeBatchRows)}, nil
}

func (w *archiveWriter) append(row logRow) error {
	if w.count == 0 || row.Time < w.minTime {
		w.minTime = row.Time
	}
	if w.count == 0 || row.Time > w.maxTime {
		w.maxTime = row.Time
	}
	w.count++
	w.pending = append(w.pending, row)
	if len(w.pending) >= writeBatchRows {
		return w.flush()
	}
	return nil
}

func (w *archiveWriter) flush() error {
	if len(w.pending) == 0 {
		return nil
	}
	if _, err := w.writer.Write(w.pending); err != nil {
		return err
	}
	w.pending = w.pending[:0]
	return nil
}

func (w *archiveWriter) finish(metadata map[string]string) error {
	if err := w.flush(); err != nil {
		return err
	}
	for k, v := range metadata {
		w.writer.SetKeyValueMetadata(k, v)
	}
	if err := w.writer.Close(); err != nil {
		return err
	}
	if err := w.file.Sync(); err != nil {
		return err
	}
	return w.file.Close()
}

func (w *archiveWriter) abort() {
	_ = w.file.Close()
}

func archiveFileName(level int, minTime, maxTime int64, node int32, seq int64) string {
	return fmt.Sprintf("L%d_%d-%d_n%d_%d%s", level, minTime/1e6, maxTime/1e6, node, seq, archiveExt)
}

func provisionalFileName(seq int64) string {
	return fmt.Sprintf("%d%s%s", seq, archiveExt, tmpExt)
}

func parseArchiveSeq(name string) (int64, bool) {
	base := strings.TrimSuffix(name, archiveExt)
	if base == name {
		return 0, false
	}
	idx := strings.LastIndexByte(base, '_')
	if idx < 0 {
		return 0, false
	}
	seq, err := strconv.ParseInt(base[idx+1:], 10, 64)
	if err != nil {
		return 0, false
	}
	return seq, true
}

func archiveFilePath(deploymentID int32, f logdb.LogFile) string {
	return filepath.Join(
		archiveDayDir(deploymentID, int32(f.Day)),
		archiveFileName(int(f.Level), f.MinTime, f.MaxTime, int32(f.Node), f.Seq),
	)
}

func readArchiveRows(path string, skip int64) iter.Seq2[logRow, error] {
	return func(yield func(logRow, error) bool) {
		f, err := os.Open(path)
		if err != nil {
			yield(logRow{}, err)
			return
		}
		defer f.Close()
		r := parquet.NewGenericReader[logRow](f)
		defer r.Close()
		if skip > 0 {
			if err := r.SeekToRow(skip); err != nil {
				yield(logRow{}, err)
				return
			}
		}
		rows := make([]logRow, writeBatchRows)
		for {
			n, err := r.Read(rows)
			for _, row := range rows[:n] {
				if !yield(row, nil) {
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) {
					yield(logRow{}, err)
				}
				return
			}
		}
	}
}

func newArchiveSeq() int64 {
	return int64(rand.Uint64() >> 1)
}

func syncDir(dir string) error {
	f, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
