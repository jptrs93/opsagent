package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// A metrics WAL is sealed only after its UTC day ends, so nothing seals within
// a run. The seed copies today's WAL to the day two days back; one day back is
// still inside the compaction grace just after midnight. The next agent start
// compacts it.
const archiveSeedScript = `
set -eu
fail() { echo "FAIL: $*"; exit 1; }
DIR=/var/lib/opendeploy-metrics
TODAY="$(date -u +%Y%m%d)"
PAST="$(date -u -d '2 days ago' +%Y%m%d)"
seed() {
  SRC="$1/$TODAY.wal"
  DST="$1/$PAST.wal"
  if [ -e "$DST" ] || [ -d "$1/$PAST" ]; then
    echo "$1: $PAST is already there"
    return 0
  fi
  for _ in $(seq 1 45); do
    [ -s "$SRC" ] && break
    sleep 2
  done
  [ -s "$SRC" ] || fail "no metrics WAL at $SRC"
  cp -p "$SRC" "$DST"
  echo "$DST: copied from $SRC ($(stat -c %s "$DST") bytes)"
}
for T in $ARCHIVE_SEED_TABLES; do
  case "$T" in
    run) seed "$DIR" ;;
    node) seed "$DIR/node" ;;
    *) fail "unknown table $T" ;;
  esac
done
`

const archiveVerifyScript = `
set -u
fail() { echo "FAIL: $*"; exit 1; }
DIR=/var/lib/opendeploy-metrics
remote_sha() {
  curl -fsS --aws-sigv4 "aws:amz:$ARCHIVE_S3_REGION:s3" --user "$ARCHIVE_S3_ACCESS_KEY_ID:$ARCHIVE_S3_SECRET_ACCESS_KEY" \
    "$ARCHIVE_S3_ENDPOINT/$ARCHIVE_S3_BUCKET/$ARCHIVE_S3_PREFIX/$1" 2>/dev/null | sha256sum | cut -d' ' -f1
}
DEADLINE=$(( $(date +%s) + ARCHIVE_WAIT_SECONDS ))
while :; do
  FILES="$(cd "$DIR" && find . -mindepth 2 -maxdepth 3 -name '*.parquet' | sed 's|^\./||' | sort)"
  COUNT=0
  RUN=0
  NODE=0
  MISSING=""
  for F in $FILES; do
    COUNT=$((COUNT + 1))
    case "$F" in node/*) NODE=$((NODE + 1)) ;; *) RUN=$((RUN + 1)) ;; esac
    [ "$(remote_sha "metrics/$F")" = "$(sha256sum "$DIR/$F" | cut -d' ' -f1)" ] || MISSING="$MISSING $F"
  done
  if [ "$RUN" -ge 1 ] && [ "$NODE" -ge 1 ] && [ -z "$MISSING" ]; then
    echo "archived=$COUNT deployment_metrics=$RUN node_metrics=$NODE"
    for F in $FILES; do echo "  $ARCHIVE_S3_PREFIX/metrics/$F"; done
    exit 0
  fi
  [ "$(date +%s)" -lt "$DEADLINE" ] || break
  sleep 5
done
echo "sealed files on the node:"
echo "$FILES"
ls -la "$DIR" "$DIR/node" 2>&1
cat /var/lib/opendeploy-run-logs/0/*.wal 2>/dev/null | tr -c '[:print:]\n' '\n' | grep -iE 'chunk|archive|compacting' | tail -40
fail "$RUN deployment and $NODE node metrics file(s) sealed; not in the bucket with the same content:$MISSING"
`

func (c *config) seedSealedMetrics(name, tables string) error {
	out, err := c.vmEnvSudoOutput(name, map[string]string{"ARCHIVE_SEED_TABLES": tables}, archiveSeedScript)
	if err != nil {
		return fmt.Errorf("seeding a past-day metrics WAL on %s failed: %w\n%s", name, err, out)
	}
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		logf("Archive seed on %s: %s", name, line)
	}
	return nil
}

func (c *config) seedPrimaryNodeMetrics() error {
	return c.seedSealedMetrics(c.PrimaryName, "node")
}

func (c *config) archiveChecks() error {
	stateFile := env("OPD_ARCHIVE_STATE_HOST", filepath.Join(c.ResultsDir, "archive.env"))
	state, err := loadShellEnvFile(stateFile)
	if err != nil {
		logf("Skipping archive checks: the flows did not turn the archive on (%s is missing)", stateFile)
		return nil
	}
	envs := map[string]string{"ARCHIVE_WAIT_SECONDS": "420"}
	for _, key := range []string{"ARCHIVE_S3_ENDPOINT", "ARCHIVE_S3_BUCKET", "ARCHIVE_S3_PREFIX", "ARCHIVE_S3_REGION", "ARCHIVE_S3_ACCESS_KEY_ID", "ARCHIVE_S3_SECRET_ACCESS_KEY"} {
		if state[key] == "" {
			return fmt.Errorf("%s is missing from %s", key, stateFile)
		}
		envs[key] = state[key]
	}
	return c.step("archive checks", "sealed metrics files on the primary and a worker reach the bucket unchanged", func() error {
		names := []string{c.PrimaryName, c.SecondaryName}
		for _, name := range names {
			if err := c.substep("seed "+name, "past-day run and node metrics WALs, then an agent restart to seal them", func() error {
				if err := c.seedSealedMetrics(name, "run node"); err != nil {
					return err
				}
				if err := c.vmRun(name, "sudo", "systemctl", "restart", "opendeploy.service"); err != nil {
					return err
				}
				return c.waitForService(name, "opendeploy.service")
			}); err != nil {
				return err
			}
		}
		if err := c.substep("primary health", c.PrimaryName, func() error { return c.waitForHealthz(c.PrimaryName) }); err != nil {
			return err
		}
		dir := filepath.Join(c.ResultsDir, "archive-checks")
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
		for _, name := range names {
			if err := c.substep("bucket "+name, "each sealed file is an object under "+state["ARCHIVE_S3_PREFIX"]+" with the same SHA-256", func() error {
				out, runErr := c.vmEnvSudoOutput(name, envs, archiveVerifyScript)
				if writeErr := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(out), 0o644); writeErr != nil {
					logf("Warning: writing %s output failed: %v", name, writeErr)
				}
				if runErr != nil {
					return fmt.Errorf("archive check on %s failed: %w\n%s", name, runErr, out)
				}
				logf("Archive check on %s passed: %s", name, strings.SplitN(strings.TrimSpace(out), "\n", 2)[0])
				return nil
			}); err != nil {
				return err
			}
		}
		return nil
	})
}
