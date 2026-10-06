package internaldeploy

import (
	"bytes"

	"github.com/jptrs93/opsagent/backend/apigen"
)

const (
	SelfBinPath                 = "/var/lib/opendeploy/bin/opendeploy"
	NetproxyStateDir            = "/var/lib/opendeploy/netproxy"
	netproxyFileDescriptorLimit = 65_536
)

func IsSelfConfig(cfg *apigen.DeploymentRecord) bool {
	return cfg != nil && IsSelfIdentity(cfg.Deployment.SpaceID, cfg.Deployment.Name)
}

func IsInternalConfig(cfg *apigen.DeploymentRecord) bool {
	return cfg != nil && IsInternalIdentity(cfg.Deployment.SpaceID, cfg.Deployment.Name)
}

// SelfSpec is the desired spec of the per-node opendeploy system deployment:
// a container spec in shape only, since the identity (space 0, SelfName)
// selects the release-binary preparer and the systemd runner.
func SelfSpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
			Source: apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{
				RemoteImage: &apigen.RemoteImage{Image: SelfImage},
			}},
			UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
		}}},
		Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST,
		},
	}
}

// IsSelfSpec reports whether a stored spec still matches SelfSpec, ignoring
// the workload version. Used to detect and repair administrator edits to the
// system deployment.
func IsSelfSpec(spec *apigen.DeploymentSpec) bool {
	if spec == nil {
		return false
	}
	want := SelfSpec()
	if err := want.SetWorkloadVersion(spec.WorkloadVersion()); err != nil {
		return false
	}
	got, err := spec.EncodeChecked()
	if err != nil {
		return false
	}
	return bytes.Equal(want.Encode(), got)
}

// NetproxySpec is the desired spec of the per-node opendeploy-net deployment.
func NetproxySpec() *apigen.DeploymentSpec {
	return &apigen.DeploymentSpec{
		Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
			Source: apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{
				RemoteImage: &apigen.RemoteImage{Image: NetproxyImage},
			}},
			Runtime: apigen.ContainerRuntime{
				OverrideCommand:     []string{"/opendeploy", "dataplane"},
				DefaultVolume:       apigen.DefaultVolumeMount{Disabled: true},
				FileDescriptorLimit: apigen.Some[uint32](netproxyFileDescriptorLimit),
				Mounts: []apigen.HostMount{{
					HostPath:      NetproxyStateDir,
					ContainerPath: NetproxyStateDir,
					Permission:    apigen.FilePermission_FILE_PERMISSION_READ_ONLY,
				}},
			},
			UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
		}}},
		Networking: apigen.NetworkingConfig{
			Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
		},
	}
}
