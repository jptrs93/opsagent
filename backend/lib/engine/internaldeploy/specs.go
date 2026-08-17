package internaldeploy

import (
	"runtime"

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
		SystemdSpec: &apigen.SystemdSpec{
			Source: &apigen.GithubRelease{
				Repo:  Repo,
				Asset: "opendeploy-linux-" + runtime.GOARCH,
			},
			Runtime: &apigen.SystemdRuntime{
				Name:    SelfName,
				BinPath: SelfBinPath,
			},
		},
		Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST,
		},
	}
}

// IsSelfSpec reports whether a stored spec still matches SelfSpec, ignoring
// workload state. Used to detect and repair administrator edits to the system
// deployment.
func IsSelfSpec(spec *apigen.DeploymentSpec) bool {
	if spec == nil || spec.SystemdSpec == nil || spec.SystemdSpec.Source == nil || spec.SystemdSpec.Runtime == nil {
		return false
	}
	gh := spec.SystemdSpec.Source
	sys := spec.SystemdSpec.Runtime
	return gh.Repo == Repo &&
		gh.Asset == "opendeploy-linux-"+runtime.GOARCH &&
		sys.Name == SelfName &&
		sys.BinPath == SelfBinPath &&
		(spec.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_HOST ||
			spec.Networking.Mode == apigen.NetworkingMode_NETWORKING_MODE_UNSPECIFIED)
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
