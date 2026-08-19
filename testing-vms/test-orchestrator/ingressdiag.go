package main

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Ingress diagnostics: a rotating headers-only packet capture on the second
// worker (the TLS ingress node) for the duration of the Playwright flows,
// plus a network-state snapshot from every cluster VM when a flow fails.
// The goal is that a one-off ingress blackhole is diagnosable from a single
// failing run: the pcap shows where SYNs died, the state snapshot shows what
// nftables/conntrack/routes held at failure time, and the tunnel probes
// separate a harness tunnel stall from a product-side drop.
//
// Everything here is best-effort — diagnostics must never fail a run.

const (
	ingressCapturePcap = "/tmp/opd-ingress-capture.pcap"
	ingressCapturePid  = "/tmp/opd-ingress-capture.pid"
	ingressCaptureLog  = "/tmp/opd-ingress-capture.log"
)

// startIngressPacketCapture starts tcpdump on the second worker, capturing
// packet headers for the ingress ports into two rotating 20MB files. The
// "any" pseudo-interface sees both the uplink and the host side of every
// veth, which is enough to localize a drop to before-DNAT, after-DNAT, or
// the missing reply leg.
func (c *config) startIngressPacketCapture() {
	script := strings.Join([]string{
		"set -e",
		ingressStopCaptureScript,
		"sudo rm -f " + ingressCapturePcap + "* " + ingressCaptureLog,
		`command -v tcpdump >/dev/null || { echo "tcpdump not installed; skipping ingress packet capture"; exit 0; }`,
		`sudo bash -c 'tcpdump -i any -nn -s 96 -B 4096 -Z root -W 2 -C 20 -w ` + ingressCapturePcap +
			` tcp port 443 or tcp port 80 </dev/null >` + ingressCaptureLog + ` 2>&1 & echo $! > ` + ingressCapturePid + `'`,
		"sleep 1",
		`sudo kill -0 "$(sudo cat ` + ingressCapturePid + `)" 2>/dev/null || { echo "ingress packet capture failed to start:"; sudo cat ` + ingressCaptureLog + `; exit 1; }`,
	}, "\n")
	if err := c.vmBash(c.Secondary2Name, script); err != nil {
		logf("Warning: starting ingress packet capture on %s failed: %v", c.Secondary2Name, err)
		return
	}
	logf("Ingress packet capture running on %s (ports 443/80, rotating 2x20MB)", c.Secondary2Name)
}

const ingressStopCaptureScript = `sudo bash -c 'if [ -f ` + ingressCapturePid + ` ]; then kill "$(cat ` + ingressCapturePid + `)" 2>/dev/null || true; rm -f ` + ingressCapturePid + `; fi'`

func (c *config) stopIngressPacketCapture() {
	if err := c.vmBash(c.Secondary2Name, ingressStopCaptureScript); err != nil {
		logf("Warning: stopping ingress packet capture on %s failed: %v", c.Secondary2Name, err)
	}
}

// captureIngressDiagnostics snapshots network state after a failed flow. It
// stops the packet capture first so the pcap files are flushed and stable
// before they are copied out.
func (c *config) captureIngressDiagnostics() {
	dir := filepath.Join(c.ResultsDir, "ingress-diagnostics")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		logf("Warning: creating ingress diagnostics dir failed: %v", err)
		return
	}
	logf("Capturing network diagnostics into %s", dir)

	for _, vm := range c.clusterVMNames() {
		out, err := c.vmCombinedOutput(vm, "sudo", "bash", "-c", ingressNetworkStateScript)
		if err != nil {
			out += fmt.Sprintf("\n(collection error: %v)\n", err)
		}
		if writeErr := os.WriteFile(filepath.Join(dir, vm+"-network-state.txt"), []byte(out), 0o644); writeErr != nil {
			logf("Warning: writing network state for %s failed: %v", vm, writeErr)
		}
	}

	c.writeIngressTunnelProbes(dir)

	c.stopIngressPacketCapture()
	for _, suffix := range []string{"0", "1"} {
		src := c.Secondary2Name + ":" + ingressCapturePcap + suffix
		dst := filepath.Join(dir, "worker-2-ingress"+suffix+".pcap")
		if err := run("limactl", "copy", src, dst); err != nil {
			logf("Note: no pcap %s to copy (%v)", src, err)
		}
	}
}

// writeIngressTunnelProbes records, side by side, whether the local ssh
// tunnel listeners accept and whether the ingress ports answer when probed
// from inside the second worker (bypassing the tunnel entirely). Together
// these distinguish "tunnel died" from "worker ingress path died".
func (c *config) writeIngressTunnelProbes(dir string) {
	var report strings.Builder
	fmt.Fprintf(&report, "captured at %s\n\n", time.Now().UTC().Format(time.RFC3339))

	for _, probe := range []struct{ label, addr string }{
		{"local TLS ingress tunnel listener", "127.0.0.1:" + c.PlaywrightTLSIngressPort},
		{"local HTTP ingress tunnel listener", "127.0.0.1:" + c.PlaywrightHTTPIngressPort},
	} {
		conn, err := net.DialTimeout("tcp", probe.addr, 3*time.Second)
		if err != nil {
			fmt.Fprintf(&report, "%s (%s): DIAL FAILED: %v\n", probe.label, probe.addr, err)
			continue
		}
		_ = conn.Close()
		fmt.Fprintf(&report, "%s (%s): accepts (note: an ssh -L listener accepts even when the remote leg is dead)\n", probe.label, probe.addr)
	}

	ip, err := c.vmIPv4(c.Secondary2Name)
	if err != nil {
		fmt.Fprintf(&report, "\nresolving %s IPv4 failed: %v\n", c.Secondary2Name, err)
	} else {
		// The same path the tunnel's remote leg uses: a host-local connection
		// to the machine's own address, through the output-chain DNAT.
		script := fmt.Sprintf(
			`for url in "https://%s:443/" "http://%s:80/"; do printf 'direct probe %%s from inside %s: ' "$url"; `+
				`curl -sk --max-time 3 -o /dev/null -w 'http_code=%%{http_code}' "$url" 2>/dev/null; `+
				`echo " exit=$?"; done`, ip, ip, c.Secondary2Name)
		out, probeErr := c.vmCombinedOutput(c.Secondary2Name, "bash", "-c", script)
		report.WriteString("\n" + out + "\n")
		if probeErr != nil {
			fmt.Fprintf(&report, "(probe error: %v)\n", probeErr)
		}

		// The same ports probed from another VM take the prerouting DNAT chain
		// instead of the output chain; disagreement between the two localizes
		// the fault to one chain's path.
		crossScript := fmt.Sprintf(
			`for url in "https://%s:443/" "http://%s:80/"; do printf 'cross-VM probe %%s from %s: ' "$url"; `+
				`curl -sk --max-time 3 -o /dev/null -w 'http_code=%%{http_code}' "$url" 2>/dev/null; `+
				`echo " exit=$?"; done`, ip, ip, c.PrimaryName)
		crossOut, crossErr := c.vmCombinedOutput(c.PrimaryName, "bash", "-c", crossScript)
		report.WriteString("\n" + crossOut + "\n")
		if crossErr != nil {
			fmt.Fprintf(&report, "(probe error: %v)\n", crossErr)
		}
		report.WriteString("curl exit=7 means refused (healthy fail-closed), exit=28 means timed out (blackhole)\n")
	}

	if err := os.WriteFile(filepath.Join(dir, "tunnel-probes.txt"), []byte(report.String()), 0o644); err != nil {
		logf("Warning: writing tunnel probes failed: %v", err)
	}
}

const ingressNetworkStateScript = `
section() { echo; echo "===== $*"; }
section date; date -u
section interfaces; ip -d addr 2>&1
section routes v4; ip route 2>&1
section routes v6; ip -6 route 2>&1
section neighbors v4; ip neigh 2>&1
section neighbors v6; ip -6 neigh 2>&1
section netns; ip netns list 2>&1
section listening sockets; ss -tlnp 2>&1; ss -ulnp 2>&1
section nftables
if command -v nft >/dev/null; then nft list ruleset 2>&1; else echo "nft not installed"; fi
section conntrack
if command -v conntrack >/dev/null; then conntrack -L 2>/dev/null | head -500
elif [ -r /proc/net/nf_conntrack ]; then head -500 /proc/net/nf_conntrack
else echo "conntrack not available"; fi
for ns in $(ip netns list 2>/dev/null | awk '{print $1}'); do
  section "netns $ns addresses"; ip netns exec "$ns" ip addr 2>&1
  section "netns $ns routes"; ip netns exec "$ns" ip route 2>&1; ip netns exec "$ns" ip -6 route 2>&1
  section "netns $ns sockets"; ip netns exec "$ns" ss -tlnp 2>&1; ip netns exec "$ns" ss -ulnp 2>&1
done
`
