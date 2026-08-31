package main

// Kernel-level assertions about the WireGuard cross-node transport, run on
// every cluster VM after the Playwright flows. On a fully keyed cluster the
// transport plan requires: the managed device configured from the map, key
// custody on disk matching the live device, no leftover ip6tnl/SIT tunnels,
// and evidence that the cross-node flows earlier in the run actually moved
// bytes through WireGuard rather than a fallback path.

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// wgCheckScript runs on one VM. It prints "transfer_total=<n>" on success so
// the orchestrator can assert cluster-wide traffic: an individual pair may
// legitimately be idle (no keepalive), but across the cluster the cross-node
// flows must have produced WireGuard transfer somewhere.
const wgCheckScript = `
set -u
fail() { echo "FAIL: $*"; exit 1; }
command -v wg >/dev/null 2>&1 || fail "wireguard-tools is not installed"

ip link show odwg0 >/dev/null 2>&1 || fail "managed WireGuard device odwg0 is missing"
MTU="$(cat /sys/class/net/odwg0/mtu)"
[ "$MTU" = "1420" ] || fail "odwg0 mtu=$MTU want 1420"

PORT="$(wg show odwg0 listen-port)"
[ "$PORT" = "$WG_EXPECTED_PORT" ] || fail "listen port $PORT want $WG_EXPECTED_PORT"

KEYFILE=/var/lib/opendeploy/wireguard.key
[ -f "$KEYFILE" ] || fail "node key file $KEYFILE is missing"
PERMS="$(stat -c '%a' "$KEYFILE")"
[ "$PERMS" = "600" ] || fail "key file mode $PERMS want 600"
DEVKEY="$(wg show odwg0 public-key)"
FILEKEY="$(wg pubkey < "$KEYFILE")"
[ "$DEVKEY" = "$FILEKEY" ] || fail "device public key does not match the key file"

PEERS="$(wg show odwg0 peers | grep -c . || true)"
[ "$PEERS" = "$WG_EXPECTED_PEERS" ] || fail "peer count $PEERS want $WG_EXPECTED_PEERS"

# Cryptokey routing is only a source check if every peer actually carries its
# routed prefixes.
EMPTY_ALLOWED="$(wg show odwg0 allowed-ips | awk 'NF < 2 || $2 == "(none)"' | grep -c . || true)"
[ "$EMPTY_ALLOWED" = "0" ] || { wg show odwg0 allowed-ips; fail "$EMPTY_ALLOWED peer(s) with empty allowed-ips"; }

# A fully keyed cluster must hold no opendeploy tunnel netdevs (names odt<id>).
LEFT="$(ip -o link show | grep -c ': odt' || true)"
[ "$LEFT" = "0" ] || { ip -o link show | grep ': odt'; fail "opendeploy ip6tnl/SIT tunnels still present"; }

TOTAL="$(wg show odwg0 transfer | awk '{ sum += $2 + $3 } END { printf "%d", sum }')"
echo "peers=$PEERS listen_port=$PORT transfer_total=$TOTAL"
`

func (c *config) wireGuardTransportChecks() error {
	dir := filepath.Join(c.ResultsDir, "wireguard-checks")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	names := c.clusterVMNames()
	envs := map[string]string{
		"WG_EXPECTED_PORT":  "51833",
		"WG_EXPECTED_PEERS": strconv.Itoa(len(names) - 1),
	}
	clusterTransfer := 0
	for _, name := range names {
		out, runErr := c.vmEnvSudoOutput(name, envs, wgCheckScript)
		if writeErr := os.WriteFile(filepath.Join(dir, name+".txt"), []byte(out), 0o644); writeErr != nil {
			logf("Warning: writing %s output failed: %v", name, writeErr)
		}
		if runErr != nil {
			return fmt.Errorf("wireguard transport check on %s failed: %w\n%s", name, runErr, out)
		}
		for _, line := range strings.Split(out, "\n") {
			if _, value, found := strings.Cut(line, "transfer_total="); found {
				if n, err := strconv.Atoi(strings.TrimSpace(value)); err == nil {
					clusterTransfer += n
				}
			}
		}
		logf("WireGuard transport check on %s passed", name)
	}
	if clusterTransfer == 0 {
		return fmt.Errorf("wireguard transport checks: no VM reported transfer; cross-node traffic did not use WireGuard")
	}
	logf("WireGuard cluster transfer total: %d bytes", clusterTransfer)
	return nil
}
