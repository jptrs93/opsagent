package logv2

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

type Appender struct {
	deploymentDir string
	version       int32
	run           int32
	stream        int8
	current       time.Time
	file          *os.File
	dropped       int64
	dropStart     time.Time
	dropLast      time.Time
	dropErr       error
}

func NewAppender(deploymentDir string, version int32, run int32, stream int8) (*Appender, error) {
	if deploymentDir == "" {
		return nil, fmt.Errorf("wal deployment dir is empty")
	}
	a := &Appender{deploymentDir: deploymentDir, version: version, run: run, stream: stream}
	if err := os.MkdirAll(deploymentDir, 0o750); err != nil {
		return nil, err
	}
	if err := a.openBucket(logBucket(time.Now().UTC())); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Appender) Append(t time.Time, line []byte) {
	if err := a.openBucket(logBucket(t)); err != nil {
		a.noteDrop(t, err)
		return
	}
	if a.dropped > 0 {
		if err := a.writeMarker(t); err != nil {
			a.noteDrop(t, err)
			return
		}
		a.dropped = 0
		a.dropErr = nil
	}
	if err := a.writeRecord(EncodeRecord(t, a.version, a.run, a.stream, line)); err != nil {
		a.noteDrop(t, err)
	}
}

func (a *Appender) Close() error {
	if a.dropped > 0 && a.file != nil {
		_ = a.writeMarker(a.dropLast)
	}
	if a.file == nil {
		return nil
	}
	err := a.file.Close()
	a.file = nil
	return err
}

func (a *Appender) writeMarker(t time.Time) error {
	msg := fmt.Sprintf("opendeploy: dropped %d log lines between %s and %s: %v\n",
		a.dropped,
		a.dropStart.UTC().Format(time.RFC3339Nano),
		a.dropLast.UTC().Format(time.RFC3339Nano),
		a.dropErr)
	return a.writeRecord(EncodeRecord(t, a.version, a.run, a.stream, []byte(msg)))
}

func (a *Appender) writeRecord(record []byte) error {
	n, err := a.file.Write(record)
	if err == nil && n != len(record) {
		err = io.ErrShortWrite
	}
	return err
}

func (a *Appender) noteDrop(t time.Time, err error) {
	if a.dropped == 0 {
		a.dropStart = t
	}
	a.dropped++
	a.dropLast = t
	a.dropErr = err
}

func (a *Appender) openBucket(bucket time.Time) error {
	if a.file != nil && a.current.Equal(bucket) {
		return nil
	}
	uid, gid, mode, err := dirMetadata(a.deploymentDir)
	if err != nil {
		return err
	}
	if a.file != nil {
		if closeErr := a.file.Close(); closeErr != nil {
			a.file = nil
			return closeErr
		}
		a.file = nil
	}
	path := filepath.Join(a.deploymentDir, bucket.Format("20060102_1504")+".wal")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_ = os.Chown(path, uid, gid)
	_ = os.Chmod(path, mode)
	a.current = bucket
	a.file = file
	return nil
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
	return int(stat.Uid), int(stat.Gid), info.Mode().Perm() & 0o660, nil
}
