package internaldeploy

import (
	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	SelfBinPath                 = "/var/lib/opendeploy/bin/opendeploy"
	NetproxyStateDir            = "/var/lib/opendeploy/netproxy"
	netproxyFileDescriptorLimit = 65_536
)

func IsSelfConfig(cfg *apigen.DeploymentConfig) bool {
	return cfg != nil && IsSelfIdentity(cfg.SpaceID, cfg.Name)
}

func IsInternalConfig(cfg *apigen.DeploymentConfig) bool {
	return cfg != nil && IsInternalIdentity(cfg.SpaceID, cfg.Name)
}

// SelfSpec is the desired spec of the per-node opendeploy system deployment.
func SelfSpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		OpendeploySpec: &apigen.OpendeploySpec{},
		Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST,
		},
	}
}

// IsSelfSpec reports whether a stored spec still matches SelfSpec, ignoring
// workload state. Used to detect and repair administrator edits to the system
// deployment.
func IsSelfSpec(spec *apigen.DeploymentSpec) bool {
	return spec != nil && spec.OpendeploySpec != nil &&
		spec.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_HOST
}

// NetproxySpec is the desired spec of the per-node opendeploy-net deployment.
func NetproxySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Container1Spec: &apigen.ContainerSpec{
			Source: apigen.ContainerBundleSource{
				RemoteImage: &apigen.RemoteDockerImage{Image: NetproxyImage},
			},
			Runtime: apigen.ContainerRuntime{
				OverrideCommand:     []string{"/opendeploy", "dataplane"},
				DefaultVolume:       apigen.DefaultVolumeMount{Disabled: true},
				FileDescriptorLimit: netproxyFileDescriptorLimit,
				Mounts: []*apigen.CustomHostMount{{
					HostPath:      NetproxyStateDir,
					ContainerPath: NetproxyStateDir,
					Permission:    apigen.FilePermission_READ_ONLY,
				}},
			},
		},
		Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		},
	}
}
