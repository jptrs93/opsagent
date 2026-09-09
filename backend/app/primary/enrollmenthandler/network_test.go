package enrollmenthandler

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestNormalizeEnrollmentUnderlay(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	primary := nodes.EnsurePrimaryNode(store, "primary", "primary-id")
	nodes.ReportNode(store, primary.Identifier, apigen.NodeReported{Identifier: primary.Identifier, UnderlayAddress: "192.0.2.1", WgPublicKey: primary.WGPublicKey, HostAddresses: primary.HostAddresses})

	got, err := nodes.NormalizeNodeUnderlay(store.Queries(), "secondary-id", " 192.0.2.2 ")
	if err != nil || got != "192.0.2.2" {
		t.Fatalf("normalized address = %q, err=%v", got, err)
	}
	if _, err := nodes.NormalizeNodeUnderlay(store.Queries(), "secondary-id", "2001:db8::2"); err == nil {
		t.Fatal("mixed-family underlay address was accepted")
	}
	if _, err := nodes.NormalizeNodeUnderlay(store.Queries(), "secondary-id", "not-an-ip"); err == nil {
		t.Fatal("invalid underlay address was accepted")
	}
	if _, err := nodes.NormalizeNodeUnderlay(store.Queries(), "secondary-id", ""); err == nil {
		t.Fatal("empty underlay address was accepted")
	}
}
