package metricstore

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	dayLayout   = "20060102"
	walExt      = ".wal"
	parquetExt  = ".parquet"
	manifestExt = ".manifest"
	tmpExt      = ".tmp"
	daySeconds  = 86400
)

func DayNumber(t time.Time) uint32 {
	return uint32(t.UTC().Unix() / daySeconds)
}

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

type sealedName struct {
	deploymentID int32
	split        bool
	nodeID       int32
	seq          int
}

func parquetName(nodeID int32, seq int) string {
	return fmt.Sprintf("n%d_%d%s", nodeID, seq, parquetExt)
}

func splitParquetName(deploymentID, nodeID int32, seq int) string {
	return fmt.Sprintf("d%d_%s", deploymentID, parquetName(nodeID, seq))
}

func manifestName(nodeID int32, seq int) string {
	return fmt.Sprintf("n%d_%d%s", nodeID, seq, manifestExt)
}

func parseNodeSeq(base string) (nodeID int32, seq int, ok bool) {
	if !strings.HasPrefix(base, "n") {
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

func parseParquetName(name string) (sealedName, bool) {
	base, found := strings.CutSuffix(name, parquetExt)
	if !found {
		return sealedName{}, false
	}
	var out sealedName
	if rest, ok := strings.CutPrefix(base, "d"); ok {
		depPart, tail, found := strings.Cut(rest, "_")
		if !found {
			return sealedName{}, false
		}
		dep, err := strconv.ParseInt(depPart, 10, 32)
		if err != nil {
			return sealedName{}, false
		}
		out.deploymentID, out.split, base = int32(dep), true, tail
	}
	var ok bool
	out.nodeID, out.seq, ok = parseNodeSeq(base)
	return out, ok
}

func parseManifestName(name string) (nodeID int32, seq int, ok bool) {
	base, found := strings.CutSuffix(name, manifestExt)
	if !found {
		return 0, 0, false
	}
	return parseNodeSeq(base)
}
