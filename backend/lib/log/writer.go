package log

import (
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type BinaryWriter struct {
	deploymentDir string
	version       int32
	run           int32
	current       time.Time
	file          *os.File
}

func NewBinaryWriter(deploymentDir string, version int32, run int32) (*BinaryWriter, error) {
	if deploymentDir == "" {
		return nil, fmt.Errorf("binary log deployment dir is empty")
	}
	w := &BinaryWriter{deploymentDir: deploymentDir, version: version, run: run}
	if err := os.MkdirAll(deploymentDir, 0o750); err != nil {
		return nil, err
	}
	if err := w.openBucket(logBucket(time.Now().UTC())); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *BinaryWriter) WriteLineAt(t time.Time, stream int8, line []byte) error {
	if len(line) > math.MaxInt32-BinaryRecordPayloadLen {
		return fmt.Errorf("log line too large: %d bytes", len(line))
	}
	if err := w.openBucket(logBucket(t)); err != nil {
		return err
	}
	record := EncodeBinaryRecord(t, w.version, w.run, stream, line)
	n, err := w.file.Write(record)
	if err == nil && n != len(record) {
		err = io.ErrShortWrite
	}
	return err
}

func (w *BinaryWriter) openBucket(bucket time.Time) error {
	if w.file != nil && w.current.Equal(bucket) {
		return nil
	}
	uid, gid, mode, err := dirMetadata(w.deploymentDir)
	if err != nil {
		return err
	}
	if w.file != nil {
		if closeErr := w.file.Close(); closeErr != nil {
			return closeErr
		}
		w.file = nil
	}
	path := filepath.Join(w.deploymentDir, fmt.Sprintf("%s_%d_%d.logbin", bucket.Format("20060102_1504"), w.version, w.run))
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_ = os.Chown(path, uid, gid)
	_ = os.Chmod(path, mode)
	w.current = bucket
	w.file = file
	return nil
}

func (w *BinaryWriter) Close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

func dirMetadata(dir string) (int, int, os.FileMode, error) {
	info, err := os.Stat(filepath.Clean(dir))
	if err != nil {
		return -1, -1, 0, err
	}
	if !info.IsDir() {
		return -1, -1, 0, fmt.Errorf("%s is not a directory", dir)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return -1, -1, 0, fmt.Errorf("stat %s: unsupported stat type", dir)
	}
	return int(stat.Uid), int(stat.Gid), fileModeForDir(info.Mode().Perm()), nil
}

func fileModeForDir(mode os.FileMode) os.FileMode {
	return mode & 0o660
}
