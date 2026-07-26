package webuihandler

import (
	"fmt"
	"net/http"
	"path/filepath"
	"strings"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/network"
	gitrepo "github.com/jptrs93/opsagent/backend/lib/repo/git"
	"github.com/jptrs93/opsagent/backend/lib/secrets"
)

var InvalidConfigErr = apigen.NewApiErr("", "invalid_config", http.StatusBadRequest)

func (h *Handler) canDeleteStaleDisconnectedSystemDeployment(cfg *apigen.DeploymentConfig) bool {
	if cfg.NodeID <= 0 || cfg.NodeID == h.NodeID || h.Cluster == nil {
		return false
	}
	_, connected := h.Cluster.ConnectedNodes()[cfg.NodeID]
	return !connected
}

// canDeleteDeployment reports whether every live assignment for the deployment
// permits deletion. Checking only the newest is not enough: mid-rollover it can
// be STOPPED while an older instance is still RUNNING.
func (h *Handler) canDeleteDeployment(cfg *apigen.DeploymentConfig, statuses []apigen.ScheduledInstanceStatus) bool {
	if len(statuses) == 0 {
		return !cfg.WorkloadRunning()
	}
	for i := range statuses {
		if !h.instancePermitsDelete(cfg, statuses[i]) {
			return false
		}
	}
	return true
}

func (h *Handler) instancePermitsDelete(cfg *apigen.DeploymentConfig, status apigen.ScheduledInstanceStatus) bool {
	if status.Runner.Status == apigen.RunningStatus_STOPPED {
		return true
	}
	if status.Runner.Status != apigen.RunningStatus_RUNNING && status.Runner.Status != apigen.RunningStatus_DEPLOYMENT_STATUS_UNKNOWN {
		return false
	}
	if cfg.NodeID <= 0 || cfg.NodeID == h.NodeID {
		return false
	}
	if h.Cluster == nil {
		return true
	}
	_, connected := h.Cluster.ConnectedNodes()[cfg.NodeID]
	return !connected
}

type deploymentAssetResolver interface {
	GetAssetByID(assetID int32) (*apigen.Asset, bool)
}

type deploymentSecretResolver interface {
	MetaByID(id int32) (secrets.Meta, bool)
}

type deploymentConfigResolver interface {
	ResolveConfig(id int32) (string, bool)
}

func (h *Handler) validateDeploymentSpec(spec *apigen.DeploymentSpec) (*apigen.DeploymentSpec, error) {
	return validateDeploymentSpecWithResolvers(spec, h.Store, h.Secrets, h.Store)
}

func validateDeploymentSpecWithAssets(spec *apigen.DeploymentSpec, assets deploymentAssetResolver) (*apigen.DeploymentSpec, error) {
	return validateDeploymentSpecWithResolvers(spec, assets, nil, nil)
}

func validateDeploymentSpecWithResolvers(spec *apigen.DeploymentSpec, assets deploymentAssetResolver, secretStore deploymentSecretResolver, configs deploymentConfigResolver) (*apigen.DeploymentSpec, error) {
	if spec == nil {
		return nil, invalidConfigErrf("spec is required")
	}
	out, err := cloneDeploymentSpec(spec)
	if err != nil {
		return nil, invalidConfigErrf("spec is invalid: %v", err)
	}
	if out.SystemdSpec != nil {
		return nil, invalidConfigErrf("systemdSpec is internal-only")
	}
	if out.Container1Spec == nil {
		return nil, invalidConfigErrf("container1Spec is required")
	}
	if out.Container2Spec != nil || out.Container3Spec != nil || out.MicroVmSpec != nil || out.VmSpec != nil {
		return nil, invalidConfigErrf("only container1Spec is currently supported")
	}
	container := out.Container1Spec
	if err := validateContainerSource(&container.Source); err != nil {
		return nil, err
	}
	if err := validateContainerSpec(container, assets); err != nil {
		return nil, err
	}
	if err := validateNetworkingConfig(&out.Networking); err != nil {
		return nil, err
	}
	if err := validateRuntimeEnvRefs(out, secretStore, configs); err != nil {
		return nil, err
	}
	return out, nil
}

func cloneDeploymentSpec(spec *apigen.DeploymentSpec) (*apigen.DeploymentSpec, error) {
	if spec == nil {
		return nil, nil
	}
	return apigen.DecodeDeploymentSpec(spec.Encode())
}

func validateNetworkingConfig(cfg *apigen.NetworkingConfig) error {
	if cfg == nil {
		return nil
	}
	if cfg.IsZero() {
		return invalidConfigErrf("networking is required")
	}
	if cfg.Mode == apigen.NetworkingMode_NETWORKING_MODE_UNSPECIFIED {
		return invalidConfigErrf("networking.mode is required")
	}
	switch cfg.Mode {
	case apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL:
		if err := validatePortForwarding(cfg.PortForwarding); err != nil {
			return err
		}
		return validateIngress(cfg.Ingress)
	case apigen.NetworkingMode_NETWORKING_MODE_HOST:
		if len(cfg.PortForwarding) > 0 {
			return invalidConfigErrf("networking.portForwarding requires virtual mode")
		}
		if len(cfg.Ingress) > 0 {
			return invalidConfigErrf("networking.ingress requires virtual mode")
		}
		return nil
	default:
		return invalidConfigErrf("networking.mode: unsupported value %d", cfg.Mode)
	}
}

func validatePortForwarding(portForwarding []*apigen.PortForward) error {
	seen := map[portForwardKey]bool{}
	for _, pf := range portForwarding {
		if pf == nil {
			continue
		}
		switch pf.Protocol {
		case apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP:
		default:
			return invalidConfigErrf("networking.portForwarding.protocol: unsupported value %d", pf.Protocol)
		}
		if pf.HostPort < 1 || pf.HostPort > 65535 {
			return invalidConfigErrf("networking.portForwarding.hostPort must be between 1 and 65535")
		}
		if pf.ContainerPort < 1 || pf.ContainerPort > 65535 {
			return invalidConfigErrf("networking.portForwarding.containerPort must be between 1 and 65535")
		}
		key := portForwardKey{protocol: pf.Protocol, hostPort: pf.HostPort}
		if seen[key] {
			return invalidConfigErrf("networking.portForwarding: duplicate %s host port %d", portForwardProtocolName(pf.Protocol), pf.HostPort)
		}
		seen[key] = true
	}
	return nil
}

const (
	defaultIngressHostPort = int32(443)
	netproxyDNSPort        = int32(53)
)

func validateIngress(ingress []*apigen.Ingress) error {
	seen := map[ingressRouteKey]bool{}
	for _, route := range ingress {
		if route == nil {
			return invalidConfigErrf("networking.ingress: entry is required")
		}
		hostname, ok := ingressHostname(route.Hostname)
		if !ok {
			return invalidConfigErrf("networking.ingress.hostname must be a valid DNS hostname")
		}
		if route.Kind != apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH {
			return invalidConfigErrf("networking.ingress.kind: unsupported value %d", route.Kind)
		}
		if route.TlsPassthroughConfig == nil {
			return invalidConfigErrf("networking.ingress.tlsPassthroughConfig is required for TLS_PASSTHROUGH")
		}
		cfg := route.TlsPassthroughConfig
		if cfg.HostPort < 0 || cfg.HostPort > 65535 {
			return invalidConfigErrf("networking.ingress.tlsPassthroughConfig.hostPort must be between 1 and 65535, or zero for the default")
		}
		if cfg.ContainerPort < 1 || cfg.ContainerPort > 65535 {
			return invalidConfigErrf("networking.ingress.tlsPassthroughConfig.containerPort must be between 1 and 65535")
		}
		hostPort := ingressHostPort(cfg.HostPort)
		if hostPort == netproxyDNSPort {
			return invalidConfigErrf("networking.ingress.tlsPassthroughConfig.hostPort %d is reserved for opendeploy-net DNS", hostPort)
		}
		key := ingressRouteKey{hostPort: hostPort, hostname: hostname}
		if seen[key] {
			return invalidConfigErrf("networking.ingress: duplicate TLS_PASSTHROUGH route for %s on host port %d", hostname, key.hostPort)
		}
		seen[key] = true
	}
	return nil
}

type ingressRouteKey struct {
	hostPort int32
	hostname string
}

func ingressHostPort(port int32) int32 {
	if port == 0 {
		return defaultIngressHostPort
	}
	return port
}

func ingressHostname(value string) (string, bool) {
	hostname := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(value)), ".")
	if hostname == "" || len(hostname) > 253 {
		return "", false
	}
	for _, label := range strings.Split(hostname, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return "", false
		}
		for _, char := range label {
			if (char < 'a' || char > 'z') && (char < '0' || char > '9') && char != '-' {
				return "", false
			}
		}
	}
	return hostname, true
}

func (h *Handler) validateNodeNetworkingClaims(nodeID, deploymentID int32, candidate *apigen.DeploymentSpec) error {
	if candidate == nil {
		return nil
	}
	routes := map[ingressRouteKey]int32{}
	tcpPorts := map[int32]int32{}
	add := func(id int32, spec apigen.DeploymentSpec) error {
		if spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			return nil
		}
		for _, pf := range spec.Networking.PortForwarding {
			if pf != nil && pf.Protocol == apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP && pf.HostPort >= 1 && pf.HostPort <= 65535 {
				if _, claimed := tcpPorts[pf.HostPort]; !claimed {
					tcpPorts[pf.HostPort] = id
				}
			}
		}
		for _, route := range spec.Networking.Ingress {
			if route == nil || route.Kind != apigen.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH || route.TlsPassthroughConfig == nil {
				continue
			}
			hostname, ok := ingressHostname(route.Hostname)
			if !ok {
				continue
			}
			key := ingressRouteKey{hostPort: ingressHostPort(route.TlsPassthroughConfig.HostPort), hostname: hostname}
			// The primary Web UI owns :443 until it and ingress share one listener.
			if id == deploymentID && nodeID == h.NodeID && key.hostPort == defaultIngressHostPort {
				return invalidConfigErrf("networking.ingress: host port %d is reserved for the primary Web UI", key.hostPort)
			}
			if owner, claimed := routes[key]; claimed && owner != id {
				return invalidConfigErrf("networking.ingress: %s on host port %d is already claimed by another deployment on this node", hostname, key.hostPort)
			}
			routes[key] = id
		}
		return nil
	}

	for _, cfg := range h.Store.FetchDeploymentSnapshot(nil) {
		if cfg.NodeID != nodeID || cfg.ID == deploymentID {
			continue
		}
		if err := add(cfg.ID, cfg.Spec); err != nil {
			return err
		}
	}
	if err := add(deploymentID, *candidate); err != nil {
		return err
	}
	for key := range routes {
		if _, claimed := tcpPorts[key.hostPort]; claimed {
			return invalidConfigErrf("networking: TCP host port %d conflicts with ingress on this node", key.hostPort)
		}
	}
	return nil
}

type portForwardKey struct {
	protocol apigen.PortForwardProtocol
	hostPort int32
}

func portForwardProtocolName(protocol apigen.PortForwardProtocol) string {
	switch protocol {
	case apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP:
		return "TCP"
	case apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP:
		return "UDP"
	default:
		return fmt.Sprintf("protocol %d", protocol)
	}
}

func validateRuntimeEnvRefs(spec *apigen.DeploymentSpec, secretStore deploymentSecretResolver, configs deploymentConfigResolver) error {
	if spec == nil || spec.Container() == nil || len(spec.Container().Runtime.EnvVars) == 0 {
		return nil
	}
	for _, value := range spec.Container().Runtime.EnvVars {
		if value.SecretID != nil {
			if secretStore == nil {
				return invalidConfigErrf("container1Spec.runtime.envVars: secrets cannot be resolved here")
			}
			if _, ok := secretStore.MetaByID(*value.SecretID); !ok {
				return invalidConfigErrf("container1Spec.runtime.envVars: unknown secret id %d", *value.SecretID)
			}
		}
		if value.ConfigID != nil {
			if configs == nil {
				return invalidConfigErrf("container1Spec.runtime.envVars: configs cannot be resolved here")
			}
			if _, ok := configs.ResolveConfig(*value.ConfigID); !ok {
				return invalidConfigErrf("container1Spec.runtime.envVars: unknown config id %d", *value.ConfigID)
			}
		}
	}
	return nil
}

func (h *Handler) validateAddressEnvRefs(nodeID, deploymentID int32, spec *apigen.DeploymentSpec, snapshot []apigen.DeploymentConfig) error {
	if spec == nil || spec.Container() == nil {
		return nil
	}
	configs := make(map[int32]*apigen.DeploymentConfig, len(snapshot))
	for i := range snapshot {
		configs[snapshot[i].ID] = &snapshot[i]
	}
	for key, value := range spec.Container().Runtime.EnvVars {
		if value == nil || value.AddressDeploymentID == nil {
			continue
		}
		targetID := *value.AddressDeploymentID
		if targetID == deploymentID && deploymentID != 0 {
			return invalidConfigErrf("container1Spec.runtime.envVars.%s: deployment cannot reference its own address", key)
		}
		target := configs[targetID]
		if target == nil || target.Deleted {
			return invalidConfigErrf("container1Spec.runtime.envVars.%s: unknown address deployment id %d", key, targetID)
		}
		if target.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			return invalidConfigErrf("container1Spec.runtime.envVars.%s: address deployment must use virtual networking", key)
		}
		if value.AddressSpaceID == nil || target.Identity.SpaceID != *value.AddressSpaceID {
			return invalidConfigErrf("container1Spec.runtime.envVars.%s: address space does not match deployment", key)
		}
	}
	return nil
}

func validateContainerSource(source *apigen.ContainerBundleSource) error {
	if source == nil {
		return invalidConfigErrf("container1Spec.source is required")
	}
	hasNixDocker := source.NixDockerBuild != nil
	hasRemoteImage := source.RemoteImage != nil
	if hasNixDocker == hasRemoteImage {
		return invalidConfigErrf("container1Spec.source: exactly one of nixDockerBuild or remoteImage must be set")
	}
	if hasNixDocker {
		if source.NixDockerBuild.Repo == "" {
			return invalidConfigErrf("container1Spec.source.nixDockerBuild: repo is required")
		}
		if source.NixDockerBuild.Flake == "" {
			return invalidConfigErrf("container1Spec.source.nixDockerBuild: flake is required")
		}
		flakePath, err := gitrepo.CleanFlakePath(source.NixDockerBuild.Flake)
		if err != nil {
			return invalidConfigErrf("container1Spec.source.nixDockerBuild.flake: %v", err)
		}
		source.NixDockerBuild.Flake = flakePath
		target := source.NixDockerBuild.Target
		if target != "" && (target != strings.TrimSpace(target) || !strings.HasPrefix(target, ".#")) {
			return invalidConfigErrf("container1Spec.source.nixDockerBuild.target: must be a local flake selector starting with .#")
		}
	}
	if hasRemoteImage {
		if source.RemoteImage.Image == "" {
			return invalidConfigErrf("container1Spec.source.remoteImage: image is required")
		}
		if source.RemoteImage.Image == internaldeploy.NetproxyImage {
			return invalidConfigErrf("container1Spec.source.remoteImage: opendeploy-net image is internal-only")
		}
	}
	return nil
}

func validateNixWorkloadVersion(spec *apigen.DeploymentSpec) error {
	if nixSource(spec) == nil {
		return nil
	}
	version := spec.WorkloadVersion()
	if version == "" {
		if spec.WorkloadRunning() {
			return invalidConfigErrf("container1Spec.version is required for a running Nix deployment")
		}
		return nil
	}
	if err := gitrepo.ValidateFullCommitHash(version); err != nil {
		return invalidConfigErrf("container1Spec.version: %v", err)
	}
	return nil
}

func (h *Handler) verifyRunningNixSource(ctx apigen.Context, spec *apigen.DeploymentSpec) error {
	nix := nixSource(spec)
	if nix == nil {
		return nil
	}
	if h.GitVersions == nil {
		return apigen.NewApiErr("Nix source verification is not configured", "nix_source_verification_unavailable", http.StatusServiceUnavailable)
	}
	if _, err := h.GitVersions.ValidateNixSource(ctx, nix.Repo, spec.WorkloadVersion(), nix.Flake); err != nil {
		return apigen.NewApiErr(fmt.Sprintf("Nix source verification failed: %v", err), "nix_source_verification_failed", http.StatusBadRequest)
	}
	return nil
}

func nixSource(spec *apigen.DeploymentSpec) *apigen.NixDockerBuild {
	if spec == nil || spec.Container() == nil {
		return nil
	}
	return spec.Container().Source.NixDockerBuild
}

func sameNixBuildConfig(a, b *apigen.NixDockerBuild) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func sameDesiredVersionSource(a, b *apigen.DeploymentSpec) bool {
	if a == nil || b == nil {
		return a == b
	}
	aContainer, bContainer := a.Container(), b.Container()
	switch {
	case aContainer != nil && bContainer != nil && aContainer.Source.NixDockerBuild != nil && bContainer.Source.NixDockerBuild != nil:
		aNix, bNix := aContainer.Source.NixDockerBuild, bContainer.Source.NixDockerBuild
		aFlake, aErr := gitrepo.CleanFlakePath(aNix.Flake)
		bFlake, bErr := gitrepo.CleanFlakePath(bNix.Flake)
		if aErr != nil || bErr != nil {
			return aNix.Repo == bNix.Repo && aNix.Flake == bNix.Flake
		}
		return aNix.Repo == bNix.Repo && aFlake == bFlake
	case aContainer != nil && bContainer != nil && aContainer.Source.RemoteImage != nil && bContainer.Source.RemoteImage != nil:
		return aContainer.Source.RemoteImage.Image == bContainer.Source.RemoteImage.Image
	case a.SystemdSpec != nil && b.SystemdSpec != nil && a.SystemdSpec.Source != nil && b.SystemdSpec.Source != nil:
		return a.SystemdSpec.Source.Repo == b.SystemdSpec.Source.Repo && a.SystemdSpec.Source.Asset == b.SystemdSpec.Source.Asset
	default:
		return false
	}
}

func validateContainerSpec(container *apigen.ContainerSpec, assets deploymentAssetResolver) error {
	if container == nil {
		return invalidConfigErrf("container1Spec is required")
	}
	validateContainerCommand(&container.Runtime)
	if err := validateEnvVars("container1Spec.runtime.envVars", container.Runtime.EnvVars); err != nil {
		return err
	}
	if err := validateContainerUpgrade(container); err != nil {
		return err
	}
	if err := validateContainerDevShmSizeKb(&container.Runtime); err != nil {
		return err
	}
	if err := validateContainerFileDescriptorLimit(&container.Runtime); err != nil {
		return err
	}
	if err := validateDefaultVolume(&container.Runtime.DefaultVolume); err != nil {
		return err
	}
	if err := resolveEnvAssetRefs("container1Spec.runtime.envVars", container.Runtime.EnvVars, assets); err != nil {
		return err
	}
	if err := validateCustomHostMounts(container.Runtime.Mounts); err != nil {
		return err
	}
	if err := validateCrossDeploymentMounts(container.Runtime.CrossDeploymentMounts); err != nil {
		return err
	}
	assetMounts, err := resolveAssetMounts(container.Runtime.AssetMounts, assets)
	if err != nil {
		return err
	}
	container.Runtime.AssetMounts = assetMounts
	return nil
}

func validateContainerCommand(cfg *apigen.ContainerRuntime) {
	if cfg == nil || len(cfg.OverrideCommand) == 0 {
		return
	}
	out := make([]string, 0, len(cfg.OverrideCommand))
	for _, arg := range cfg.OverrideCommand {
		arg = strings.TrimSpace(arg)
		if arg != "" {
			out = append(out, arg)
		}
	}
	cfg.OverrideCommand = out
}

func validateContainerUpgrade(cfg *apigen.ContainerSpec) error {
	if cfg == nil {
		return nil
	}
	switch cfg.UpgradeStrategy {
	case apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_UNSPECIFIED:
		cfg.UpgradeStrategy = apigen.ContainerUpgradeStrategy_RECREATE
	case apigen.ContainerUpgradeStrategy_RECREATE:
		cfg.ReadinessSignal = nil
	case apigen.ContainerUpgradeStrategy_ROLLOVER:
		if cfg.Runtime.EnvVars != nil {
			if _, ok := cfg.Runtime.EnvVars["OPENDEPLOY_READINESS_SOCK_PATH"]; ok {
				return invalidConfigErrf("container1Spec.runtime.envVars: OPENDEPLOY_READINESS_SOCK_PATH is reserved for rollover readiness")
			}
		}
		if cfg.ReadinessSignal == nil {
			cfg.ReadinessSignal = &apigen.ContainerReadinessSignal{}
		}
		if cfg.ReadinessSignal.TimeoutSeconds < 0 {
			return invalidConfigErrf("container1Spec.readinessSignal.timeoutSeconds must be non-negative")
		}
	default:
		return invalidConfigErrf("container1Spec.upgradeStrategy: unsupported value %d", cfg.UpgradeStrategy)
	}
	return nil
}

func validateContainerDevShmSizeKb(cfg *apigen.ContainerRuntime) error {
	if cfg == nil || cfg.DevShmSizeKb == 0 {
		return nil
	}
	if cfg.DevShmSizeKb < 0 {
		return invalidConfigErrf("container1Spec.runtime.devShmSizeKb must be non-negative")
	}
	return nil
}

func validateContainerFileDescriptorLimit(cfg *apigen.ContainerRuntime) error {
	if cfg == nil || cfg.FileDescriptorLimit == 0 {
		return nil
	}
	if cfg.FileDescriptorLimit < 0 {
		return invalidConfigErrf("container1Spec.runtime.fileDescriptorLimit must be non-negative")
	}
	return nil
}

func validateDefaultVolume(mount *apigen.DefaultVolumeMount) error {
	if mount == nil || mount.ContainerPath == "" {
		return nil
	}
	path, err := cleanContainerPath(mount.ContainerPath)
	if err != nil {
		return invalidConfigErrf("container1Spec.runtime.defaultVolume.containerPath: %v", err)
	}
	mount.ContainerPath = path
	return nil
}

func validateCustomHostMounts(mounts []*apigen.CustomHostMount) error {
	for _, m := range mounts {
		if m == nil || strings.TrimSpace(m.HostPath) == "" || strings.TrimSpace(m.ContainerPath) == "" {
			return invalidConfigErrf("container1Spec.runtime.mounts: hostPath and containerPath are both required")
		}
		host := strings.TrimSpace(m.HostPath)
		container := strings.TrimSpace(m.ContainerPath)
		if !filepath.IsAbs(host) {
			return invalidConfigErrf("container1Spec.runtime.mounts: hostPath must be absolute")
		}
		if filepath.Clean(host) != host || host == "/" {
			return invalidConfigErrf("container1Spec.runtime.mounts: hostPath must be a clean absolute path")
		}
		cleaned, err := cleanContainerPath(container)
		if err != nil {
			return invalidConfigErrf("container1Spec.runtime.mounts.containerPath: %v", err)
		}
		if containerHostMountDenied(host) {
			return invalidConfigErrf("container1Spec.runtime.mounts: host path %q is not allowed", host)
		}
		if !validMountPermission(m.Permission) {
			return invalidConfigErrf("container1Spec.runtime.mounts: permission is required")
		}
		m.HostPath = host
		m.ContainerPath = cleaned
	}
	return nil
}

func validateCrossDeploymentMounts(mounts []*apigen.CrossDeploymentMount) error {
	for _, mount := range mounts {
		if mount == nil || mount.DeploymentID <= 0 {
			return invalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: deploymentId is required")
		}
		path, err := cleanContainerPath(mount.ContainerPath)
		if err != nil {
			return invalidConfigErrf("container1Spec.runtime.crossDeploymentMounts.containerPath: %v", err)
		}
		if !validMountPermission(mount.Permission) {
			return invalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: permission is required")
		}
		mount.ContainerPath = path
	}
	return nil
}

func cleanContainerPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return "", fmt.Errorf("must be a clean absolute path other than root")
	}
	return path, nil
}

func validMountPermission(permission apigen.FilePermission) bool {
	return permission == apigen.FilePermission_READ_WRITE || permission == apigen.FilePermission_READ_ONLY
}

var deniedContainerHostMountRoots = []string{
	"/boot",
	"/dev",
	"/etc",
	"/proc",
	"/root",
	"/run",
	"/sys",
	"/var/run",
	"/var/lib/containerd",
	"/var/lib/docker",
	"/var/lib/opendeploy",
	"/var/lib/opendeploy-assets",
	"/var/lib/opendeploy-build-logs",
	"/var/lib/opendeploy-containerd",
	"/var/lib/opendeploy-releases",
	"/var/lib/opendeploy-run-logs",
	"/var/lib/opendeploy-volumes",
}

func containerHostMountDenied(host string) bool {
	host = filepath.Clean(host)
	for _, root := range deniedContainerHostMountRoots {
		if pathEqualOrUnder(host, root) {
			return true
		}
	}
	return false
}

func (h *Handler) validateCrossDeploymentMountSources(spec *apigen.DeploymentSpec, nodeID, currentID int32) error {
	if spec == nil || spec.Container() == nil {
		return nil
	}
	for _, mount := range spec.Container().Runtime.CrossDeploymentMounts {
		if mount == nil {
			continue
		}
		if mount.DeploymentID == currentID && currentID != 0 {
			return invalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: a deployment cannot mount its own default volume")
		}
		source := h.findConfigByID(mount.DeploymentID)
		if source == nil || source.Deleted {
			return invalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: source deployment %d does not exist", mount.DeploymentID)
		}
		if source.NodeID != nodeID {
			return invalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: source deployment %d is on a different node", mount.DeploymentID)
		}
		container := source.Spec.Container()
		if container == nil || container.Runtime.DefaultVolume.Disabled {
			return invalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: source deployment %d has no default volume", mount.DeploymentID)
		}
	}
	return nil
}

func pathEqualOrUnder(path, root string) bool {
	root = filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func resolveAssetMounts(in []*apigen.AssetMount, assets deploymentAssetResolver) ([]*apigen.AssetMount, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if assets == nil {
		return nil, invalidConfigErrf("container1Spec.runtime.assetMounts: assets cannot be resolved here")
	}
	out := make([]*apigen.AssetMount, 0, len(in))
	for _, m := range in {
		if m == nil {
			return nil, invalidConfigErrf("container1Spec.runtime.assetMounts: asset and path are both required")
		}
		path := strings.TrimSpace(m.ContainerPath)
		if m.AssetID <= 0 || path == "" {
			return nil, invalidConfigErrf("container1Spec.runtime.assetMounts: assetId and path are both required")
		}
		if !filepath.IsAbs(path) {
			return nil, invalidConfigErrf("container1Spec.runtime.assetMounts: path must be absolute")
		}
		cleanPath := filepath.Clean(path)
		if cleanPath != path || cleanPath == "/" || strings.HasSuffix(path, "/") {
			return nil, invalidConfigErrf("container1Spec.runtime.assetMounts: path must be an absolute file path")
		}
		asset, ok := assets.GetAssetByID(m.AssetID)
		if !ok {
			return nil, invalidConfigErrf("container1Spec.runtime.assetMounts: asset id %d not found", m.AssetID)
		}
		if m.Permission != apigen.FilePermission_READ_ONLY && m.Permission != apigen.FilePermission_READ_EXECUTE {
			return nil, invalidConfigErrf("container1Spec.runtime.assetMounts: permission must be READ_ONLY or READ_EXECUTE")
		}
		out = append(out, &apigen.AssetMount{AssetID: asset.ID, ContainerPath: cleanPath, Permission: m.Permission})
	}
	return out, nil
}

func resolveEnvAssetRefs(scope string, env map[string]*apigen.EnvVarValue, assets deploymentAssetResolver) error {
	for key, value := range env {
		if value.AssetID <= 0 {
			continue
		}
		if assets == nil {
			return invalidConfigErrf("%s.%s: assets cannot be resolved here", scope, key)
		}
		asset, ok := assets.GetAssetByID(value.AssetID)
		if !ok {
			return invalidConfigErrf("%s.%s: asset id %d not found", scope, key, value.AssetID)
		}
		value.Asset = asset.Key
		value.AssetID = asset.ID
	}
	return nil
}

// validateEnvVars trims and validates env keys and typed values. Duplicate keys
// after trimming are rejected so the resulting process environment is unambiguous.
func validateEnvVars(scope string, in map[string]*apigen.EnvVarValue) error {
	seen := make(map[string]struct{}, len(in))
	out := make(map[string]*apigen.EnvVarValue, len(in))
	for rawKey, value := range in {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			return invalidConfigErrf("%s: key is required", scope)
		}
		if _, dup := seen[key]; dup {
			return invalidConfigErrf("%s: duplicate key %q", scope, key)
		}
		seen[key] = struct{}{}
		if value == nil {
			return invalidConfigErrf("%s.%s: value is required", scope, key)
		}
		set := 0
		if value.Value != nil {
			set++
		}
		if value.SecretID != nil {
			set++
			if *value.SecretID <= 0 {
				return invalidConfigErrf("%s.%s: secretId must be positive", scope, key)
			}
		}
		if value.ConfigID != nil {
			set++
			if *value.ConfigID <= 0 {
				return invalidConfigErrf("%s.%s: configId must be positive", scope, key)
			}
		}
		if value.AssetID > 0 {
			set++
		}
		hasAddress := value.AddressDeploymentID != nil || value.AddressSpaceID != nil
		if hasAddress {
			set++
			if value.AddressDeploymentID == nil || value.AddressSpaceID == nil {
				return invalidConfigErrf("%s.%s: addressDeploymentId and addressSpaceId are required together", scope, key)
			}
			if *value.AddressDeploymentID <= 0 {
				return invalidConfigErrf("%s.%s: addressDeploymentId must be positive", scope, key)
			}
			if *value.AddressDeploymentID > network.MaxDeploymentID {
				return invalidConfigErrf("%s.%s: addressDeploymentId must not exceed %d", scope, key, network.MaxDeploymentID)
			}
			if *value.AddressSpaceID < 0 || *value.AddressSpaceID > network.MaxSpaceID {
				return invalidConfigErrf("%s.%s: addressSpaceId must be between 0 and %d", scope, key, network.MaxSpaceID)
			}
		}
		if set != 1 {
			return invalidConfigErrf("%s.%s: exactly one of value, secretId, configId, assetId, or address is required", scope, key)
		}
		out[key] = value
	}
	for key := range in {
		delete(in, key)
	}
	for key, value := range out {
		in[key] = value
	}
	return nil
}

func invalidConfigErrf(format string, args ...any) error {
	e := InvalidConfigErr
	msg := fmt.Sprintf(format, args...)
	e.InternalErr = msg
	e.DisplayErr = msg
	return e
}
