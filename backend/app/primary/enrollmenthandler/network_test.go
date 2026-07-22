package enrollmenthandler

import (
	"path/filepath"
	"testing"

	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

func TestNormalizeEnrollmentUnderlay(t *testing.T) {
	store := sqlite.NewPrimaryStorage(filepath.Join(t.TempDir(), "primary.db"))
	defer store.Close()
	primary := store.EnsurePrimaryNode("primary", "primary-id")
	store.MustSetNodeAddresses(primary.ID, []string{"192.0.2.1"})
	h := &Handler{store: store}

	got, err := h.normalizeEnrollmentUnderlay("worker-id", " 192.0.2.2 ")
	if err != nil || got != "192.0.2.2" {
		t.Fatalf("normalized address = %q, err=%v", got, err)
	}
	if _, err := h.normalizeEnrollmentUnderlay("worker-id", "2001:db8::2"); err == nil {
		t.Fatal("mixed-family underlay address was accepted")
	}
	if _, err := h.normalizeEnrollmentUnderlay("worker-id", "not-an-ip"); err == nil {
		t.Fatal("invalid underlay address was accepted")
	}
	if got, err := h.normalizeEnrollmentUnderlay("legacy-worker", ""); err != nil || got != "" {
		t.Fatalf("empty legacy underlay = %q, err=%v", got, err)
	}
}
