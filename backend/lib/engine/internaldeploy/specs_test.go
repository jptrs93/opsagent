package internaldeploy

import (
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

func TestSystemSpecsValidate(t *testing.T) {
	for name, spec := range map[string]*apigen.DeploymentSpec{"self": SelfSpec(), "netproxy": NetproxySpec()} {
		if err := spec.Validate(); err != nil {
			t.Fatalf("%s spec invalid: %v", name, err)
		}
	}
}

func TestIsSelfSpecIgnoresWorkloadVersion(t *testing.T) {
	spec := SelfSpec()
	if err := spec.SetWorkloadVersion("v1.2.3"); err != nil {
		t.Fatal(err)
	}
	if !IsSelfSpec(spec) {
		t.Fatal("versioned self spec not recognised")
	}
	if IsSelfSpec(NetproxySpec()) {
		t.Fatal("netproxy spec recognised as the self spec")
	}
	edited := SelfSpec()
	edited.Networking.Mode = apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL
	if IsSelfSpec(edited) {
		t.Fatal("edited self spec recognised as the self spec")
	}
	if IsSelfSpec(&apigen.DeploymentSpec{}) {
		t.Fatal("invalid spec recognised as the self spec")
	}
}
