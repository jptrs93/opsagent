package metricstore

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	dayLayout  = "20060102"
	walExt     = ".wal"
	parquetExt = ".parquet"
	tmpExt     = ".tmp"
)

func utcDay(t time.Time) time.Time {
	t = t.UTC()
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC)
}

func walPath(dir string, day time.Time) string {
	return filepath.Join(dir, day.UTC().Format(dayLayout)+walExt)
}

func dayDir(dir string, day time.Time) string {
	return filepath.Join(dir, day.UTC().Format(dayLayout))
}

func parseDay(name string) (time.Time, bool) {
	t, err := time.ParseInLocation(dayLayout, name, time.UTC)
	if err != nil {
		return time.Time{}, false
	}
	return t, true
}

func parseWALName(name string) (time.Time, bool) {
	base, ok := strings.CutSuffix(name, walExt)
	if !ok {
		return time.Time{}, false
	}
	return parseDay(base)
}

func parquetName(nodeID int32, seq int) string {
	return fmt.Sprintf("n%d_%d%s", nodeID, seq, parquetExt)
}

func parseParquetName(name string) (nodeID int32, seq int, ok bool) {
	base, found := strings.CutSuffix(name, parquetExt)
	if !found || !strings.HasPrefix(base, "n") {
		return 0, 0, false
	}
	nodePart, seqPart, found := strings.Cut(base[1:], "_")
	if !found {
		return 0, 0, false
	}
	n, err := strconv.ParseInt(nodePart, 10, 32)
	if err != nil {
		return 0, 0, false
	}
	s, err := strconv.Atoi(seqPart)
	if err != nil {
		return 0, 0, false
	}
	return int32(n), s, true
}
