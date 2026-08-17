package clusterhandler

import (
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestStampLegacyIdentityDualWritesWireFormat(t *testing.T) {
	st := &apigen.ScheduledInstanceState{
		Config: apigen.DeploymentConfig{ID: 16, SpaceID: 5, Name: "radkit-postgres"},
	}
	stampLegacyIdentity(st)

	decoded, err := apigen.DecodeScheduledInstanceState(st.Encode())
	if err != nil {
		t.Fatalf("decode stamped assignment: %v", err)
	}
	if got := decoded.Config.LegacyIdentity; got.SpaceID != 5 || got.Name != "radkit-postgres" {
		t.Fatalf("legacy identity on the wire = (%d, %q), want (5, %q)", got.SpaceID, got.Name, "radkit-postgres")
	}
	if decoded.Config.SpaceID != 5 || decoded.Config.Name != "radkit-postgres" {
		t.Fatalf("flat identity fields = (%d, %q), want (5, %q)", decoded.Config.SpaceID, decoded.Config.Name, "radkit-postgres")
	}
}

func TestStampLegacyIdentitySkipsEmptyConfig(t *testing.T) {
	st := &apigen.ScheduledInstanceState{}
	stampLegacyIdentity(st)
	if !st.Config.LegacyIdentity.IsZero() {
		t.Fatal("legacy identity stamped on an assignment with no config")
	}
}
