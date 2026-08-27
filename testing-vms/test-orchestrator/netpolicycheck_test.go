package main

import (
	"os/exec"
	"strings"
	"testing"
)

// The kernel checks only ever run inside a VM at the end of a full E2E run, so
// a shell syntax error in one of them would surface seven minutes into a run
// that already passed. Parse them here instead.
func TestNetworkPolicyCheckScriptsParse(t *testing.T) {
	if !cmdExists("bash") {
		t.Skip("bash is not installed")
	}
	for _, check := range netpolChecks {
		t.Run(check.name, func(t *testing.T) {
			cmd := exec.Command("bash", "-n")
			cmd.Stdin = strings.NewReader(netpolPrelude + check.script)
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("script does not parse: %v\n%s", err, out)
			}
		})
	}
}

func TestNetworkPolicyChecksAreOrdered(t *testing.T) {
	// The spoof check leaves the nonzero anti-spoofing counter that the
	// skeleton check reads, and the netaudit check leaves a flushed chain that
	// only the restart check rebuilds.
	positions := map[string]int{}
	for i, check := range netpolChecks {
		positions[check.name] = i
	}
	for _, pair := range [][2]string{
		{"spoofed-source-dropped", "skeleton-and-elements-present"},
		{"netaudit-reports-divergence", "agent-restart-rederives-filter"},
	} {
		before, okBefore := positions[pair[0]]
		after, okAfter := positions[pair[1]]
		if !okBefore || !okAfter {
			t.Fatalf("missing check in pair %v", pair)
		}
		if before >= after {
			t.Fatalf("%s must run before %s", pair[0], pair[1])
		}
	}
}
