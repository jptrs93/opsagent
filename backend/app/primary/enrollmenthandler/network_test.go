package enrollmenthandler

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
)

func TestNormalizeEnrollmentUnderlay(t *testing.T) {
	store := state.Open(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	primary := store.EnsurePrimaryNode("primary", "primary-id")
	store.MustSetNodeAddresses(primary.ID, []string{"192.0.2.1"})

	got, err := store.NormalizeNodeUnderlay("worker-id", " 192.0.2.2 ")
	if err != nil || got != "192.0.2.2" {
		t.Fatalf("normalized address = %q, err=%v", got, err)
	}
	if _, err := store.NormalizeNodeUnderlay("worker-id", "2001:db8::2"); err == nil {
		t.Fatal("mixed-family underlay address was accepted")
	}
	if _, err := store.NormalizeNodeUnderlay("worker-id", "not-an-ip"); err == nil {
		t.Fatal("invalid underlay address was accepted")
	}
	if got, err := store.NormalizeNodeUnderlay("legacy-worker", ""); err != nil || got != "" {
		t.Fatalf("empty legacy underlay = %q, err=%v", got, err)
	}
}
