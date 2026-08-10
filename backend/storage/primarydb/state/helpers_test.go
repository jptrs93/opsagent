package state

import "github.com/jptrs93/opsagent/backend/apigen"

// The literal keeps the assertion independent of internaldeploy.NetproxySpec,
// so an accidental change to the shipped limit fails here.
const netproxyFileDescriptorLimit = 65_536

// nonEmptySpec returns a valid spec that encodes to non-empty bytes.
func nonEmptySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{
			Source:  apigen.ContainerBundleSource{RemoteImage: &apigen.RemoteDockerImage{Image: "example/app"}},
			Runtime: apigen.ContainerRuntime{User: "1000"},
		},
		Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
	}
}

func testSpecWithState(version string, running bool) *apigen.DeploymentSpec {
	spec := nonEmptySpec()
	if err := spec.SetWorkloadState(version, running); err != nil {
		panic(err)
	}
	return spec
}
