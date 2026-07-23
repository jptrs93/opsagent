package apigen

import (
	"fmt"
	"strconv"
	"strings"
)

const legacyDeploymentVolumePrefix = "/var/lib/opendeploy-volumes/"

// ValidateDeploymentConfig2 validates the structural invariants that protobuf
// cannot express without oneof fields.
func ValidateDeploymentConfig2(in *DeploymentConfig2) error {
	if in == nil {
		return fmt.Errorf("deployment config is nil")
	}
	if err := ValidateDeploymentSpec2(&in.Spec); err != nil {
		return fmt.Errorf("deployment config %d: %w", in.ID, err)
	}
	return nil
}

// ValidateDeploymentSpec2 requires one current workload variant and one source
// for each container. Future multi-container support can relax the total count
// while retaining the rule that workload types cannot be mixed.
func ValidateDeploymentSpec2(in *DeploymentSpec2) error {
	if in == nil {
		return fmt.Errorf("deployment spec is nil")
	}
	variants := []struct {
		name      string
		container *ContainerSpec
		set       bool
	}{
		{name: "container_1_spec", container: in.Container1Spec, set: in.Container1Spec != nil},
		{name: "container_2_spec", container: in.Container2Spec, set: in.Container2Spec != nil},
		{name: "container_3_spec", container: in.Container3Spec, set: in.Container3Spec != nil},
		{name: "micro_vm_spec", set: in.MicroVmSpec != nil},
		{name: "vm_spec", set: in.VmSpec != nil},
		{name: "systemd_spec", set: in.SystemdSpec != nil},
	}
	count := 0
	for _, variant := range variants {
		if variant.set {
			count++
		}
	}
	if count != 1 {
		return fmt.Errorf("DeploymentSpec2 has %d workload fields; exactly one is required", count)
	}
	for _, variant := range variants {
		if variant.container != nil {
			if err := validateContainerSpec2(variant.name, variant.container); err != nil {
				return err
			}
		}
	}
	if in.SystemdSpec != nil {
		if in.SystemdSpec.Source == nil {
			return fmt.Errorf("DeploymentSpec2.systemd_spec.source is required")
		}
		if in.SystemdSpec.Runtime == nil {
			return fmt.Errorf("DeploymentSpec2.systemd_spec.runtime is required")
		}
	}
	return nil
}

func validateContainerSpec2(name string, in *ContainerSpec) error {
	sourceCount := 0
	if in.Source.NixDockerBuild != nil {
		sourceCount++
	}
	if in.Source.RemoteImage != nil {
		sourceCount++
	}
	if sourceCount != 1 {
		return fmt.Errorf("DeploymentSpec2.%s.source has %d fields; exactly one is required", name, sourceCount)
	}
	for i, mount := range in.Runtime.CrossDeploymentMounts {
		if mount == nil {
			return fmt.Errorf("DeploymentSpec2.%s.runtime.cross_deployment_mounts[%d] is required", name, i)
		}
		if !validVolumePermission(mount.Permission) {
			return fmt.Errorf("DeploymentSpec2.%s.runtime.cross_deployment_mounts[%d].permission is invalid", name, i)
		}
	}
	for i, mount := range in.Runtime.Mounts {
		if mount == nil {
			return fmt.Errorf("DeploymentSpec2.%s.runtime.mounts[%d] is required", name, i)
		}
		if !validVolumePermission(mount.Permission) {
			return fmt.Errorf("DeploymentSpec2.%s.runtime.mounts[%d].permission is invalid", name, i)
		}
	}
	for i, mount := range in.Runtime.AssetMounts {
		if mount == nil {
			return fmt.Errorf("DeploymentSpec2.%s.runtime.asset_mounts[%d] is required", name, i)
		}
		if mount.Permission != FilePermission_READ_ONLY && mount.Permission != FilePermission_READ_EXECUTE {
			return fmt.Errorf("DeploymentSpec2.%s.runtime.asset_mounts[%d].permission must be READ_ONLY or READ_EXECUTE", name, i)
		}
	}
	return nil
}

func validVolumePermission(permission FilePermission) bool {
	switch permission {
	case FilePermission_READ_WRITE, FilePermission_READ_ONLY:
		return true
	default:
		return false
	}
}

// DeploymentConfigToV2 converts a V1 deployment config without silently
// discarding fields that DeploymentConfig2 cannot represent.
func DeploymentConfigToV2(in *DeploymentConfig) (*DeploymentConfig2, error) {
	if in == nil {
		return nil, nil
	}
	spec, err := DeploymentSpecToV2(&in.Spec, in.DesiredState)
	if err != nil {
		return nil, fmt.Errorf("deployment config %d: %w", in.ID, err)
	}
	return &DeploymentConfig2{
		ID:        in.ID,
		NodeID:    in.NodeID,
		Identity:  in.Identity,
		CreatedAt: in.CreatedAt,
		UpdatedAt: in.UpdatedAt,
		UpdatedBy: in.UpdatedBy,
		Version:   in.Version,
		Spec:      *spec,
		Deleted:   in.Deleted,
	}, nil
}

// DeploymentConfigFromV2 converts a V2 deployment config to its V1 shape.
func DeploymentConfigFromV2(in *DeploymentConfig2) (*DeploymentConfig, error) {
	if in == nil {
		return nil, nil
	}
	spec, desired, err := DeploymentSpecFromV2(&in.Spec)
	if err != nil {
		return nil, fmt.Errorf("deployment config %d: %w", in.ID, err)
	}
	return &DeploymentConfig{
		ID:           in.ID,
		NodeID:       in.NodeID,
		Identity:     in.Identity,
		CreatedAt:    in.CreatedAt,
		UpdatedAt:    in.UpdatedAt,
		UpdatedBy:    in.UpdatedBy,
		Version:      in.Version,
		Spec:         *spec,
		DesiredState: desired,
		Deleted:      in.Deleted,
	}, nil
}

// DeploymentSpecToV2 converts a V1 deployment spec and its separate desired
// state into the V2 shape, where version and running state belong to the
// selected workload spec.
func DeploymentSpecToV2(in *DeploymentSpec, desired DesiredState) (*DeploymentSpec2, error) {
	if in == nil {
		return nil, nil
	}
	prepareCount := 0
	if in.Prepare.GithubRelease != nil {
		prepareCount++
	}
	if in.Prepare.ContainerImage != nil {
		prepareCount++
	}
	if in.Prepare.NixDockerBuild != nil {
		prepareCount++
	}
	if prepareCount != 1 {
		return nil, fmt.Errorf("deployment spec has %d prepare sources; exactly one is required", prepareCount)
	}

	out := &DeploymentSpec2{Networking: *cloneNetworkingConfig(&in.Networking)}
	if source := in.Prepare.GithubRelease; source != nil {
		if !in.Runner.Container.IsZero() {
			return nil, fmt.Errorf("GitHub release source with a container runner is not representable in DeploymentSpec2")
		}
		out.SystemdSpec = &SystemdSpec{
			Source:  &GithubRelease{Repo: source.Repo, Asset: source.Asset},
			Runtime: &SystemdRuntime{Name: in.Runner.Systemd.Name, BinPath: in.Runner.Systemd.BinPath},
			Version: desired.Version,
			Running: desired.Running,
		}
		if err := ValidateDeploymentSpec2(out); err != nil {
			return nil, err
		}
		return out, nil
	}
	if !in.Runner.Systemd.IsZero() {
		return nil, fmt.Errorf("container source with a systemd runner is not representable in DeploymentSpec2")
	}

	container, err := containerSpecToV2(in)
	if err != nil {
		return nil, err
	}
	container.Version = desired.Version
	container.Running = desired.Running
	out.Container1Spec = container
	if err := ValidateDeploymentSpec2(out); err != nil {
		return nil, err
	}
	return out, nil
}

// DeploymentSpecFromV2 converts the currently supported V2 workload variants
// and returns the V1 desired state split out from the workload spec.
func DeploymentSpecFromV2(in *DeploymentSpec2) (*DeploymentSpec, DesiredState, error) {
	if in == nil {
		return nil, DesiredState{}, fmt.Errorf("deployment spec is nil")
	}
	if err := ValidateDeploymentSpec2(in); err != nil {
		return nil, DesiredState{}, err
	}
	if in.Container2Spec != nil || in.Container3Spec != nil {
		return nil, DesiredState{}, fmt.Errorf("multiple containers are not representable in DeploymentSpec")
	}
	if in.MicroVmSpec != nil {
		return nil, DesiredState{}, fmt.Errorf("microVM workloads are not representable in DeploymentSpec")
	}
	if in.VmSpec != nil {
		return nil, DesiredState{}, fmt.Errorf("VM workloads are not representable in DeploymentSpec")
	}

	out := &DeploymentSpec{Networking: *cloneNetworkingConfig(&in.Networking)}
	if systemd := in.SystemdSpec; systemd != nil {
		out.Prepare.GithubRelease = &GithubReleaseConfig{Repo: systemd.Source.Repo, Asset: systemd.Source.Asset}
		out.Runner.Systemd = SystemdRunnerConfig{Name: systemd.Runtime.Name, BinPath: systemd.Runtime.BinPath}
		return out, DesiredState{Version: systemd.Version, Running: systemd.Running}, nil
	}

	desired, err := containerSpecFromV2(out, in.Container1Spec)
	if err != nil {
		return nil, DesiredState{}, err
	}
	return out, desired, nil
}

func containerSpecToV2(in *DeploymentSpec) (*ContainerSpec, error) {
	runtime, err := containerRuntimeToV2(&in.Runner.Container)
	if err != nil {
		return nil, err
	}
	out := &ContainerSpec{
		Runtime:         runtime,
		UpgradeStrategy: in.Runner.Container.UpgradeStrategy,
		ReadinessSignal: clonePtr(in.Runner.Container.ReadinessSignal),
	}
	if source := in.Prepare.NixDockerBuild; source != nil {
		out.Source.NixDockerBuild = &NixDockerBuild2{Repo: source.Repo, Flake: source.Flake, Target: source.Target}
	} else if source := in.Prepare.ContainerImage; source != nil {
		out.Source.RemoteImage = &RemoteDockerImage{Image: source.Image}
	}
	return out, nil
}

func containerSpecFromV2(out *DeploymentSpec, in *ContainerSpec) (DesiredState, error) {
	if in == nil {
		return DesiredState{}, fmt.Errorf("DeploymentSpec2.container1_spec is required")
	}
	sourceCount := 0
	if in.Source.NixDockerBuild != nil {
		sourceCount++
	}
	if in.Source.RemoteImage != nil {
		sourceCount++
	}
	if sourceCount != 1 {
		return DesiredState{}, fmt.Errorf("ContainerSpec has %d bundle sources; exactly one is required", sourceCount)
	}
	if source := in.Source.NixDockerBuild; source != nil {
		out.Prepare.NixDockerBuild = &NixDockerBuildConfig{Repo: source.Repo, Flake: source.Flake, Target: source.Target}
	} else {
		source := in.Source.RemoteImage
		out.Prepare.ContainerImage = &ContainerImageConfig{Image: source.Image}
	}
	runtime, err := containerRuntimeFromV2(&in.Runtime)
	if err != nil {
		return DesiredState{}, err
	}
	runtime.UpgradeStrategy = in.UpgradeStrategy
	runtime.ReadinessSignal = clonePtr(in.ReadinessSignal)
	out.Runner.Container = runtime
	return DesiredState{Version: in.Version, Running: in.Running}, nil
}

func containerRuntimeToV2(in *ContainerRunnerConfig) (ContainerRuntime, error) {
	out := ContainerRuntime{
		User:                in.User,
		EnvVars:             envVarsToV2(in.EnvVars),
		OverrideCommand:     cloneSlice(in.Command),
		OverrideWorkingDir:  in.WorkingDir,
		DefaultVolume:       DefaultVolumeMount{ContainerPath: in.DataMountPath, Disabled: in.DisableDataVolume},
		Mounts:              make([]*CustomHostMount, 0, len(in.Mounts)),
		AssetMounts:         make([]*AssetMount2, 0, len(in.AssetMounts)),
		DevShmSizeKb:        in.DevShmSizeKb,
		FileDescriptorLimit: in.FileDescriptorLimit,
	}
	if in.Mounts == nil {
		out.Mounts = nil
	}
	for _, mount := range in.Mounts {
		if mount == nil {
			out.Mounts = append(out.Mounts, nil)
			continue
		}
		permission := FilePermission_READ_WRITE
		if mount.Readonly {
			permission = FilePermission_READ_ONLY
		}
		if deploymentID, ok := legacyDeploymentVolumeID(mount.Host); ok {
			out.CrossDeploymentMounts = append(out.CrossDeploymentMounts, &CrossDeploymentMount{
				DeploymentID:  deploymentID,
				ContainerPath: mount.Container,
				Permission:    permission,
			})
			continue
		}
		out.Mounts = append(out.Mounts, &CustomHostMount{
			HostPath:      mount.Host,
			ContainerPath: mount.Container,
			Permission:    permission,
		})
	}
	if in.AssetMounts == nil {
		out.AssetMounts = nil
	}
	for _, mount := range in.AssetMounts {
		if mount == nil {
			out.AssetMounts = append(out.AssetMounts, nil)
			continue
		}
		permission := FilePermission_READ_ONLY
		if mount.Executable {
			permission = FilePermission_READ_EXECUTE
		}
		out.AssetMounts = append(out.AssetMounts, &AssetMount2{
			AssetID:       mount.AssetID,
			ContainerPath: mount.Path,
			Permission:    permission,
		})
	}
	return out, nil
}

func containerRuntimeFromV2(in *ContainerRuntime) (ContainerRunnerConfig, error) {
	out := ContainerRunnerConfig{
		User:                in.User,
		EnvVars:             envVarsFromV2(in.EnvVars),
		Command:             cloneSlice(in.OverrideCommand),
		WorkingDir:          in.OverrideWorkingDir,
		DataMountPath:       in.DefaultVolume.ContainerPath,
		DisableDataVolume:   in.DefaultVolume.Disabled,
		Mounts:              make([]*ContainerMount, 0, len(in.CrossDeploymentMounts)+len(in.Mounts)),
		AssetMounts:         make([]*ContainerAssetMount, 0, len(in.AssetMounts)),
		DevShmSizeKb:        in.DevShmSizeKb,
		FileDescriptorLimit: in.FileDescriptorLimit,
	}
	if in.CrossDeploymentMounts == nil && in.Mounts == nil {
		out.Mounts = nil
	}
	for i, mount := range in.CrossDeploymentMounts {
		if mount == nil {
			out.Mounts = append(out.Mounts, nil)
			continue
		}
		readonly, err := legacyMountReadonly(mount.Permission)
		if err != nil {
			return ContainerRunnerConfig{}, fmt.Errorf("container runtime cross_deployment_mounts[%d]: %w", i, err)
		}
		out.Mounts = append(out.Mounts, &ContainerMount{
			Host:      fmt.Sprintf("%s%d/default", legacyDeploymentVolumePrefix, mount.DeploymentID),
			Container: mount.ContainerPath,
			Readonly:  readonly,
		})
	}
	for i, mount := range in.Mounts {
		if mount == nil {
			out.Mounts = append(out.Mounts, nil)
			continue
		}
		readonly, err := legacyMountReadonly(mount.Permission)
		if err != nil {
			return ContainerRunnerConfig{}, fmt.Errorf("container runtime mounts[%d]: %w", i, err)
		}
		out.Mounts = append(out.Mounts, &ContainerMount{Host: mount.HostPath, Container: mount.ContainerPath, Readonly: readonly})
	}
	if in.AssetMounts == nil {
		out.AssetMounts = nil
	}
	for i, mount := range in.AssetMounts {
		if mount == nil {
			out.AssetMounts = append(out.AssetMounts, nil)
			continue
		}
		var executable bool
		switch mount.Permission {
		case FilePermission_READ_ONLY:
		case FilePermission_READ_EXECUTE:
			executable = true
		default:
			return ContainerRunnerConfig{}, fmt.Errorf("container runtime asset_mounts[%d] permission %d is not representable in DeploymentSpec", i, mount.Permission)
		}
		out.AssetMounts = append(out.AssetMounts, &ContainerAssetMount{AssetID: mount.AssetID, Path: mount.ContainerPath, Executable: executable})
	}
	return out, nil
}

func legacyDeploymentVolumeID(host string) (int32, bool) {
	relative, ok := strings.CutPrefix(host, legacyDeploymentVolumePrefix)
	if !ok {
		return 0, false
	}
	id, suffix, ok := strings.Cut(relative, "/")
	if !ok || suffix != "default" || id == "" {
		return 0, false
	}
	parsed, err := strconv.ParseInt(id, 10, 32)
	if err != nil || parsed <= 0 || strconv.FormatInt(parsed, 10) != id {
		return 0, false
	}
	return int32(parsed), true
}

func legacyMountReadonly(permission FilePermission) (bool, error) {
	switch permission {
	case FilePermission_READ_WRITE:
		return false, nil
	case FilePermission_READ_ONLY:
		return true, nil
	default:
		return false, fmt.Errorf("permission %d is not representable in DeploymentSpec", permission)
	}
}

func envVarsToV2(in map[string]*EnvVarValue) map[string]*EnvVarValue2 {
	if in == nil {
		return nil
	}
	out := make(map[string]*EnvVarValue2, len(in))
	for key, value := range in {
		if value == nil {
			out[key] = nil
			continue
		}
		out[key] = &EnvVarValue2{
			SecretID:            clonePtr(value.SecretID),
			ConfigID:            clonePtr(value.ConfigID),
			Value:               clonePtr(value.Value),
			Asset:               value.Asset,
			AssetID:             value.AssetID,
			AddressDeploymentID: clonePtr(value.AddressDeploymentID),
			AddressSpaceID:      clonePtr(value.AddressSpaceID),
		}
	}
	return out
}

func envVarsFromV2(in map[string]*EnvVarValue2) map[string]*EnvVarValue {
	if in == nil {
		return nil
	}
	out := make(map[string]*EnvVarValue, len(in))
	for key, value := range in {
		if value == nil {
			out[key] = nil
			continue
		}
		out[key] = &EnvVarValue{
			SecretID:            clonePtr(value.SecretID),
			ConfigID:            clonePtr(value.ConfigID),
			Value:               clonePtr(value.Value),
			Asset:               value.Asset,
			AssetID:             value.AssetID,
			AddressDeploymentID: clonePtr(value.AddressDeploymentID),
			AddressSpaceID:      clonePtr(value.AddressSpaceID),
		}
	}
	return out
}

func cloneNetworkingConfig(in *NetworkingConfig) *NetworkingConfig {
	if in == nil {
		return nil
	}
	out := &NetworkingConfig{
		Mode:           in.Mode,
		PortForwarding: make([]*PortForward, 0, len(in.PortForwarding)),
		Ingress:        make([]*Ingress, 0, len(in.Ingress)),
	}
	if in.PortForwarding == nil {
		out.PortForwarding = nil
	}
	for _, forward := range in.PortForwarding {
		out.PortForwarding = append(out.PortForwarding, clonePtr(forward))
	}
	if in.Ingress == nil {
		out.Ingress = nil
	}
	for _, ingress := range in.Ingress {
		if ingress == nil {
			out.Ingress = append(out.Ingress, nil)
			continue
		}
		copy := *ingress
		copy.TlsPassthroughConfig = clonePtr(ingress.TlsPassthroughConfig)
		out.Ingress = append(out.Ingress, &copy)
	}
	return out
}

func clonePtr[T any](in *T) *T {
	if in == nil {
		return nil
	}
	out := *in
	return &out
}

func cloneSlice[T any](in []T) []T {
	if in == nil {
		return nil
	}
	return append([]T(nil), in...)
}
