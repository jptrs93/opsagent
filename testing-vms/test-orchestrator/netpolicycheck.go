package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Kernel-level assertions about the network policy boundary, run on the second
// worker after the Playwright flows. They live here rather than in a flow
// because the Playwright container cannot read nftables counters, enter a
// workload network namespace, or restart an agent — and because the last two
// checks deliberately break kernel state, which no later flow could tolerate.
//
// The workload ids come from the flow through a results-directory env file:
// the checks address workloads the way the kernel does (netns
// `opendeploy-<id>-v<version>`, veth `od<id>s<slot>`) and cannot authenticate
// to the API to look them up.

const netpolConnectScript = `
import socket, sys
dst, port = sys.argv[1], int(sys.argv[2])
src = sys.argv[3] if len(sys.argv) > 3 else None
family = socket.AF_INET6 if ":" in dst else socket.AF_INET
s = socket.socket(family, socket.SOCK_STREAM)
s.settimeout(5)
try:
    if src:
        s.bind((src, 0))
    s.connect((dst, port))
    sys.exit(0)
except socket.timeout:
    sys.exit(1)
except OSError as e:
    print("connect error: %s" % e, file=sys.stderr)
    sys.exit(2)
`

// netpolPrelude defines the shared helpers every check builds on. A dropped
// packet is silent, so `connect` distinguishes a timeout (exit 1 — what a drop
// looks like) from a refusal or unreachable (exit 2 — something other than the
// boundary).
const netpolPrelude = `
set -uo pipefail
fail() { echo "CHECK FAILED: $*" >&2; exit 1; }
inconclusive() { echo "CHECK INCONCLUSIVE: $*" >&2; exit 0; }

cat >/tmp/opd-netpol-connect.py <<'PYEOF'
` + netpolConnectScript + `
PYEOF

# Container ids (and so netns names) are opendeploy-<dep>-<version>-<si>-<run>;
# the highest run wins when several are present.
ns_for() { ls /run/netns 2>/dev/null | grep -E "^opendeploy-$1-[0-9]+-[0-9]+-[0-9]+$" | sort -t- -k5,5n | tail -1; }

addr6_of() { ip netns exec "$1" ip -6 -o addr show dev eth0 scope global 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1; }
addr4_of() { ip netns exec "$1" ip -4 -o addr show dev eth0 2>/dev/null | awk '{print $4}' | cut -d/ -f1 | head -1; }

# counter_for <family> <chain> <match>: packet count of the first counted rule
# in that chain matching the substring.
counter_for() {
  nft list chain "$1" opendeploy "$2" 2>/dev/null \
    | grep -F -- "$3" | grep -o 'counter packets [0-9]*' | awk '{print $3}' | head -1
}

# log_mark: a timestamp comparable with the agent's log lines, which carry
# local time in the same layout.
log_mark() { date '+%Y-%m-%dT%H:%M:%S'; }

# agent_log_since <mark> <pattern>: the agent's own log lines matching pattern
# and written at or after mark.
#
# The agent writes slog output to its run-log store, not to journald — run-log
# directory 0 is the agent itself, the rest are deployment ids. Only stderr
# (panics) reaches the journal, so journalctl -u opendeploy never sees an audit
# report however correctly the audit fires. The WAL is a binary container
# around JSON lines, hence the tr.
#
# Filtering by time rather than counting: the store rotates into half-hourly
# buckets and archives the old ones, so a count taken before an event can be
# HIGHER than the count after it, and "wait for the count to grow" would hang
# until the deadline however correctly the audit fires.
agent_log_since() {
  cat /var/lib/opendeploy-run-logs/0/*.wal 2>/dev/null \
    | tr -c '[:print:]\n' '\n' | grep -a "$2" \
    | awk -v m="$1" 'match($0, /"time":"[^"]+"/) {
        t = substr($0, RSTART + 8, RLENGTH - 9)
        if (substr(t, 1, 19) >= m) print
      }'
}

# connect <netns> <dst> <port> [src]
connect() {
  local ns="$1"; shift
  ip netns exec "$ns" python3 /tmp/opd-netpol-connect.py "$@"
}

SERVER_NS="$(ns_for "$NETPOL_SERVER_ID")"
PEER_NS="$(ns_for "$NETPOL_PEER_ID")"
[ -n "$SERVER_NS" ] || fail "no network namespace for server deployment $NETPOL_SERVER_ID"
[ -n "$PEER_NS" ] || fail "no network namespace for peer deployment $NETPOL_PEER_ID"
SERVER_CHAIN="wl_dst_${NETPOL_SERVER_ID}"
echo "server_ns=$SERVER_NS peer_ns=$PEER_NS server_addr=$NETPOL_SERVER_ADDRESS chain=$SERVER_CHAIN"
`

// The order matters: the spoof check is what puts a nonzero count on the
// anti-spoofing drop rule that the skeleton check then reads, and the netaudit
// check leaves the destination chain flushed for the restart check to rebuild.
var netpolChecks = []struct {
	name        string
	description string
	script      string
}{
	{
		name:        "spoofed-source-dropped",
		description: "a workload cannot emit another workload's logical source address",
		script: `
# Control: same-space traffic to the server is allowed, so anything this check
# observes afterwards is about the source address, not about reachability.
connect "$PEER_NS" "$NETPOL_SERVER_ADDRESS" "$NETPOL_SERVER_PORT" \
  || fail "same-space control connection from the peer failed (rc=$?)"

PEER_ADDR="$(addr6_of "$PEER_NS")"
[ -n "$PEER_ADDR" ] || fail "peer namespace has no global IPv6 address"
# An unassigned address inside the peer's own /64: same space, so the
# destination policy would accept it. Only the (veth, source) pair check can
# drop it.
SPOOF="$(python3 -c 'import ipaddress,sys; n=ipaddress.ip_network(sys.argv[1]+"/64", strict=False); print(str(n.network_address + 0xdead))' "$PEER_ADDR")"
BEFORE="$(counter_for ip6 forward '@src_ok')"
[ -n "$BEFORE" ] || fail "no counted anti-spoofing rule in the ip6 forward chain"

# nodad: without it the address stays tentative while duplicate address
# detection runs, and binding a tentative source fails with EADDRNOTAVAIL —
# which connect reports as rc=2, indistinguishable here from "the boundary let
# it through". The packet has to actually reach the veth for this check to say
# anything about anti-spoofing.
ip netns exec "$PEER_NS" ip -6 addr add "$SPOOF"/128 dev eth0 nodad || fail "adding the spoofed address failed"
connect "$PEER_NS" "$NETPOL_SERVER_ADDRESS" "$NETPOL_SERVER_PORT" "$SPOOF"
RC=$?
ip netns exec "$PEER_NS" ip -6 addr del "$SPOOF"/128 dev eth0 || true

[ "$RC" = "1" ] || fail "connection from spoofed source $SPOOF was not dropped (rc=$RC)"
AFTER="$(counter_for ip6 forward '@src_ok')"
[ "${AFTER:-0}" -gt "${BEFORE:-0}" ] || fail "anti-spoofing drop counter did not advance ($BEFORE -> $AFTER)"
echo "spoofed source $SPOOF dropped; counter $BEFORE -> $AFTER"
`,
	},
	{
		name:        "v4-container-to-container-dropped",
		description: "the machine-local IPv4 path stays egress-only",
		script: `
SERVER_V4="$(addr4_of "$SERVER_NS")"
[ -n "$SERVER_V4" ] || fail "server namespace has no machine-local IPv4 address"
BEFORE="$(counter_for ip forward 'daddr')"
[ -n "$BEFORE" ] || fail "no counted machine-local IPv4 rule in the ip forward chain"

connect "$PEER_NS" "$SERVER_V4" "$NETPOL_SERVER_PORT"
RC=$?
[ "$RC" = "1" ] || fail "container-to-container IPv4 connection to $SERVER_V4 was not dropped (rc=$RC)"
AFTER="$(counter_for ip forward 'daddr')"
[ "${AFTER:-0}" -gt "${BEFORE:-0}" ] || fail "machine-local IPv4 drop counter did not advance ($BEFORE -> $AFTER)"
echo "IPv4 container-to-container to $SERVER_V4 dropped; counter $BEFORE -> $AFTER"
`,
	},
	{
		name:        "drop-counters-attributable",
		description: "the denied cross-space probe is a counted drop in the destination's own chain",
		script: `
BEFORE="$(counter_for ip6 "$SERVER_CHAIN" 'drop')"
[ -n "$BEFORE" ] || fail "no counted drop rule in $SERVER_CHAIN"
# Two probe intervals of the global-space probe, which the flow leaves denied.
sleep 15
AFTER="$(counter_for ip6 "$SERVER_CHAIN" 'drop')"
[ "${AFTER:-0}" -gt "${BEFORE:-0}" ] \
  || fail "default-deny counter in $SERVER_CHAIN did not advance ($BEFORE -> $AFTER); the denied probe's traffic is not reaching this rule"
echo "$SERVER_CHAIN default deny counter $BEFORE -> $AFTER"
`,
	},
	{
		name:        "skeleton-and-elements-present",
		description: "the static forward program persists and every attachment is represented",
		script: `
FORWARD="$(nft list chain ip6 opendeploy forward)"
echo "$FORWARD" | grep -q 'ct state established,related accept' || fail "conntrack accept missing from the ip6 forward chain"
echo "$FORWARD" | grep -q '@blocked_out' || fail "blocked_out drop missing from the ip6 forward chain"
echo "$FORWARD" | grep -q '@src_ok' || fail "anti-spoofing drop missing from the ip6 forward chain"
echo "$FORWARD" | grep -q 'vmap @dst_dispatch' || fail "destination dispatch missing from the ip6 forward chain"

# The veth slot suffix moves with rollovers (s0 for the first live network, s1
# for a concurrent candidate), so discover the live name rather than assuming
# one. src_ok is a concatenated set and renders its ifnames; the plain ifname
# set does not.
SERVER_VETH="$(nft list set ip6 opendeploy src_ok | grep -oE "od${NETPOL_SERVER_ID}s[0-9]+" | head -1)"
[ -n "$SERVER_VETH" ] || fail "server veth missing from the src_ok set"
# Ask the kernel for membership instead of grepping the listing: nft 1.0.9
# renders every element of a bare 'type ifname' set as "", so a grep over
# 'list set managed' can never match however correct the set is. 'get element'
# succeeds only if the key is really there (ENOENT otherwise).
nft get element ip6 opendeploy managed { "$SERVER_VETH" } >/dev/null 2>&1 \
  || fail "server veth $SERVER_VETH missing from the managed set"
nft list map ip6 opendeploy dst_dispatch | grep -q "$NETPOL_SERVER_ADDRESS" || fail "server inbound address missing from the dispatch map"

# The counters carry the whole run's history: a skeleton rebuilt on every
# reconcile would have reset them.
SPOOF_COUNT="$(counter_for ip6 forward '@src_ok')"
[ "${SPOOF_COUNT:-0}" -gt 0 ] || fail "anti-spoofing counter is zero; the static skeleton was rebuilt and lost its counters"
echo "static program intact; anti-spoofing counter $SPOOF_COUNT"
`,
	},
	{
		name:        "netaudit-reports-divergence",
		description: "netaudit detects filter-chain divergence and does not repair it",
		script: `
RULES_BEFORE="$(nft list chain ip6 opendeploy "$SERVER_CHAIN" | grep -c -E 'accept|drop')"
[ "$RULES_BEFORE" -gt 0 ] || fail "$SERVER_CHAIN has no rules to flush"
# Mark the log before the flush: the store carries the whole run, so presence
# alone would be satisfied by a report from an earlier check.
MARK="$(log_mark)"
nft flush chain ip6 opendeploy "$SERVER_CHAIN" || fail "flushing $SERVER_CHAIN failed"
echo "flushed $SERVER_CHAIN ($RULES_BEFORE rules)"

# The audit runs every 60s and rechecks once after 2s before reporting.
FOUND=""
DEADLINE=$((SECONDS+240))
while [ $SECONDS -lt $DEADLINE ]; do
  LINE="$(agent_log_since "$MARK" 'kernel network state diverged' | tail -1)"
  if [ -n "$LINE" ]; then FOUND="$LINE"; break; fi
  if [ "$(nft list chain ip6 opendeploy "$SERVER_CHAIN" | grep -c -E 'accept|drop')" -gt 0 ]; then
    inconclusive "an unrelated reconcile rebuilt $SERVER_CHAIN before the audit ran"
  fi
  sleep 5
done
[ -n "$FOUND" ] || fail "netaudit did not report divergence within 240s of flushing $SERVER_CHAIN"
echo "$FOUND" | grep -q 'missing_filter=\[[^]]' || fail "divergence report did not name the missing filter rules: $FOUND"

# Log-only by design: divergence is evidence of a bug or of interference and
# must stay visible rather than be silently repaired.
[ "$(nft list chain ip6 opendeploy "$SERVER_CHAIN" | grep -c -E 'accept|drop')" = "0" ] \
  || fail "netaudit repaired $SERVER_CHAIN; the audit must be log-only"
echo "netaudit reported divergence and left the chain alone"
`,
	},
	{
		name:        "agent-restart-rederives-filter",
		description: "filter state is re-derived from recovered attachments after an agent restart",
		script: `
MARK="$(log_mark)"
systemctl restart opendeploy || fail "restarting the agent failed"

# The chain, the src_ok elements and the dispatch entry are re-derived
# independently, so each needs the same deadline: waiting only for the chain
# and then sampling the other two once fails whenever the agent happens to
# publish them a moment later.
DEADLINE=$((SECONDS+180))
while [ $SECONDS -lt $DEADLINE ]; do
  nft list chain ip6 opendeploy "$SERVER_CHAIN" 2>/dev/null | grep -q drop \
    && nft list set ip6 opendeploy src_ok 2>/dev/null | grep -q "od${NETPOL_SERVER_ID}s" \
    && nft list map ip6 opendeploy dst_dispatch 2>/dev/null | grep -q "$NETPOL_SERVER_ADDRESS" \
    && break
  sleep 5
done
nft list chain ip6 opendeploy "$SERVER_CHAIN" 2>/dev/null | grep -q drop \
  || fail "$SERVER_CHAIN was not re-derived within 180s of the agent restart"
nft list set ip6 opendeploy src_ok | grep -q "od${NETPOL_SERVER_ID}s" \
  || fail "src_ok elements were not re-derived for the server attachment"
nft list map ip6 opendeploy dst_dispatch | grep -q "$NETPOL_SERVER_ADDRESS" \
  || fail "dispatch entry was not re-derived for the server attachment"

connect "$PEER_NS" "$NETPOL_SERVER_ADDRESS" "$NETPOL_SERVER_PORT" \
  || fail "same-space traffic did not recover after the agent restart (rc=$?)"

DEADLINE=$((SECONDS+180))
SYNC=""
while [ $SECONDS -lt $DEADLINE ]; do
  SYNC="$(agent_log_since "$MARK" 'kernel network state in sync' | tail -1)"
  [ -n "$SYNC" ] && break
  sleep 5
done
[ -n "$SYNC" ] || fail "netaudit did not report in-sync state within 180s of the rebuild"
echo "$SYNC"
`,
	},
}

func (c *config) networkPolicyKernelChecks() error {
	stateFile := env("OPD_NETPOLICY_STATE_HOST", filepath.Join(c.ResultsDir, "netpolicy.env"))
	state, err := loadShellEnvFile(stateFile)
	if err != nil || state["NETPOL_SERVER_ID"] == "" {
		logf("Skipping network policy kernel checks: %s has no workload ids", stateFile)
		return nil
	}
	envs := map[string]string{
		"NETPOL_SERVER_ID":      state["NETPOL_SERVER_ID"],
		"NETPOL_PEER_ID":        state["NETPOL_PEER_ID"],
		"NETPOL_PROBE_ID":       state["NETPOL_PROBE_ID"],
		"NETPOL_SERVER_PORT":    state["NETPOL_SERVER_PORT"],
		"NETPOL_SERVER_ADDRESS": state["NETPOL_SERVER_ADDRESS"],
	}
	for key, value := range envs {
		if value == "" {
			logf("Skipping network policy kernel checks: %s is missing from %s", key, stateFile)
			return nil
		}
	}

	dir := filepath.Join(c.ResultsDir, "network-policy-checks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for _, check := range netpolChecks {
		out, runErr := c.vmEnvSudoOutput(c.Secondary2Name, envs, netpolPrelude+check.script)
		if writeErr := os.WriteFile(filepath.Join(dir, check.name+".txt"), []byte(out), 0o644); writeErr != nil {
			logf("Warning: writing %s output failed: %v", check.name, writeErr)
		}
		if runErr != nil {
			return fmt.Errorf("network policy check %s (%s) failed: %w\n%s", check.name, check.description, runErr, out)
		}
		if strings.Contains(out, "CHECK INCONCLUSIVE") {
			logf("Network policy check %s was inconclusive; see %s", check.name, filepath.Join(dir, check.name+".txt"))
			continue
		}
		logf("Network policy check %s passed", check.name)
	}
	return nil
}

func (c *config) vmEnvSudoOutput(name string, envs map[string]string, script string) (string, error) {
	args := []string{"limactl", "shell", "--tty=false", name, "sudo", "env"}
	for _, key := range sortedKeys(envs) {
		args = append(args, key+"="+envs[key])
	}
	args = append(args, "bash", "-s")
	cmd := exec.Command(args[0], args[1:]...)
	cmd.Stdin = strings.NewReader(script)
	out, err := cmd.CombinedOutput()
	return string(out), err
}
