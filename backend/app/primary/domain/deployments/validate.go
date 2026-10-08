package deployments

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/assets"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/values"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/pq"
	"net/http"
	"net/netip"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/secrets"
	"github.com/jptrs93/opsagent/backend/lib/engine/imageref"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
	"github.com/jptrs93/opsagent/backend/lib/network"
	gitrepo "github.com/jptrs93/opsagent/backend/lib/repo/git"
)

var InvalidConfigErr = apigen.NewApiErr("", "invalid_config", http.StatusBadRequest)

// SecretRefOutsideSpaceErr refuses a deployment write pinning a secret version
// outside the deployment's own space and the global space.
var SecretRefOutsideSpaceErr = apigen.NewApiErr("Deployment references a secret outside its own or the global space", "secret_reference_outside_space", http.StatusBadRequest)

var ConfigRefOutsideSpaceErr = apigen.NewApiErr("Deployment references a config outside its own or the global space", "config_reference_outside_space", http.StatusBadRequest)

var AssetRefOutsideSpaceErr = apigen.NewApiErr("Deployment references an asset outside its own or the global space", "asset_reference_outside_space", http.StatusBadRequest)

func canDeleteStaleDisconnectedSystemDeployment(cluster NodeConnectivity, primaryNodeID uint64, cfg *apigen.DeploymentRecord) bool {
	if cfg.Deployment.PlacementNodeID() == 0 || cfg.Deployment.PlacementNodeID() == primaryNodeID || cluster == nil {
		return false
	}
	_, connected := cluster.ConnectedNodes()[cfg.Deployment.PlacementNodeID()]
	return !connected
}

// canDeleteDeployment reports whether every live assignment for the deployment
// permits deletion. Checking only the newest is not enough: mid-rollover it can
// be STOPPED while an older instance is still RUNNING.
func canDeleteDeployment(cluster NodeConnectivity, primaryNodeID uint64, cfg *apigen.DeploymentRecord, statuses []apigen.ScheduledInstanceStatus) bool {
	if len(statuses) == 0 {
		return !cfg.WorkloadRunning()
	}
	for i := range statuses {
		if !instancePermitsDelete(cluster, primaryNodeID, cfg, statuses[i]) {
			return false
		}
	}
	return true
}

func instancePermitsDelete(cluster NodeConnectivity, primaryNodeID uint64, cfg *apigen.DeploymentRecord, status apigen.ScheduledInstanceStatus) bool {
	running := status.Runner.Value.Status
	if running == apigen.RunningStatus_RUNNING_STATUS_STOPPED {
		return true
	}
	if running != apigen.RunningStatus_RUNNING_STATUS_RUNNING && running != apigen.RunningStatus_RUNNING_STATUS_UNSPECIFIED {
		return false
	}
	if cfg.Deployment.PlacementNodeID() == 0 || cfg.Deployment.PlacementNodeID() == primaryNodeID {
		return false
	}
	if cluster == nil {
		return true
	}
	_, connected := cluster.ConnectedNodes()[cfg.Deployment.PlacementNodeID()]
	return !connected
}

type AssetResolver interface {
	// GetAssetVersionRef resolves the immutable asset values deployment specs
	// pin.
	GetAssetVersionRef(ref apigen.ValueRef) (assets.AssetVersionRef, bool)
}

type SecretResolver interface {
	MetaByRef(ref apigen.ValueRef) (secrets.Meta, bool)
}

type ConfigResolver interface {
	ResolveConfig(ref apigen.ValueRef) (string, bool)
}

type queryResolver struct{ q *pq.Queries }

func (r queryResolver) GetAssetVersionRef(ref apigen.ValueRef) (assets.AssetVersionRef, bool) {
	return assets.GetAssetVersionRef(r.q, ref)
}

func (r queryResolver) ResolveConfig(ref apigen.ValueRef) (string, bool) {
	version, ok := values.GetConfigVersion(r.q, ref)
	if !ok {
		return "", false
	}
	return version.Value, true
}

func ValidateSpec(q *pq.Queries, secretStore *secrets.Manager, spec *apigen.DeploymentSpec) (*apigen.DeploymentSpec, error) {
	resolver := queryResolver{q}
	return ValidateSpecWithResolvers(spec, resolver, secretStore, resolver)
}

func ValidateSpecWithResolvers(spec *apigen.DeploymentSpec, assets AssetResolver, secretStore SecretResolver, configs ConfigResolver) (*apigen.DeploymentSpec, error) {
	if spec == nil {
		return nil, InvalidConfigErrf("spec is required")
	}
	out, err := cloneDeploymentSpec(spec)
	if err != nil {
		return nil, InvalidConfigErrf("spec is invalid: %v", err)
	}
	container := out.Workload.Value.Container
	if container == nil {
		return nil, InvalidConfigErrf("container1Spec is required")
	}
	if err := validateContainerSource(&container.Source); err != nil {
		return nil, err
	}
	if err := validateContainerSpec(container, assets); err != nil {
		return nil, err
	}
	if err := validateNetworkingConfig(&out.Networking, secretStore); err != nil {
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
	b, err := spec.EncodeChecked()
	if err != nil {
		return nil, err
	}
	return apigen.DecodeDeploymentSpec(b)
}

func validateNetworkingConfig(cfg *apigen.NetworkingConfig, secretStore SecretResolver) error {
	if cfg == nil {
		return nil
	}
	switch cfg.Mode {
	case apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL:
		if err := validatePortForwarding(cfg.PortForwarding); err != nil {
			return err
		}
		return validateIngress(cfg.Ingress, secretStore)
	case apigen.NetworkingMode_NETWORKING_MODE_HOST:
		if len(cfg.PortForwarding) > 0 {
			return InvalidConfigErrf("networking.portForwarding requires virtual mode")
		}
		if len(cfg.Ingress) > 0 {
			return InvalidConfigErrf("networking.ingress requires virtual mode")
		}
		return nil
	default:
		return InvalidConfigErrf("networking.mode: unsupported value %d", cfg.Mode)
	}
}

func validatePortForwarding(portForwarding []apigen.PortForward) error {
	seen := map[portForwardKey]bool{}
	for i := range portForwarding {
		pf := &portForwarding[i]
		switch pf.Protocol {
		case apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP:
		default:
			return InvalidConfigErrf("networking.portForwarding.protocol: unsupported value %d", pf.Protocol)
		}
		if pf.HostPort < 1 || pf.HostPort > 65535 {
			return InvalidConfigErrf("networking.portForwarding.hostPort must be between 1 and 65535")
		}
		if pf.ContainerPort < 1 || pf.ContainerPort > 65535 {
			return InvalidConfigErrf("networking.portForwarding.containerPort must be between 1 and 65535")
		}
		if err := validatePortForwardIpFilter(pf.IpFilter); err != nil {
			return err
		}
		key := portForwardKey{protocol: pf.Protocol, hostPort: pf.HostPort}
		if seen[key] {
			return InvalidConfigErrf("networking.portForwarding: duplicate %s host port %d", portForwardProtocolName(pf.Protocol), pf.HostPort)
		}
		seen[key] = true
	}
	return nil
}

func validatePortForwardIpFilter(filters []apigen.IpFilter) error {
	seen := map[netip.Prefix]bool{}
	for i := range filters {
		filter := &filters[i]
		switch filter.Mode {
		case apigen.IpFilterMode_IP_FILTER_MODE_ALLOW:
		case apigen.IpFilterMode_IP_FILTER_MODE_DENY:
			return InvalidConfigErrf("networking.portForwarding.ipFilter.deny is not supported yet")
		default:
			return InvalidConfigErrf("networking.portForwarding.ipFilter.mode: unsupported value %d", filter.Mode)
		}
		prefix := filter.Prefix.Prefix()
		if !prefix.IsValid() {
			return InvalidConfigErrf("networking.portForwarding.ipFilter.allow: entry is not a valid IP address or CIDR prefix")
		}
		if prefix.Addr().Is4In6() {
			return InvalidConfigErrf("networking.portForwarding.ipFilter.allow: %q must use the plain IPv4 form", network.FilterEntryString(prefix))
		}
		if !filter.Prefix.Masked() {
			return InvalidConfigErrf("networking.portForwarding.ipFilter.allow: %q has host bits set", filter.Prefix.Prefix())
		}
		if seen[prefix] {
			return InvalidConfigErrf("networking.portForwarding.ipFilter.allow: duplicate entry %q", network.FilterEntryString(prefix))
		}
		seen[prefix] = true
		filter.Prefix = apigen.PrefixOf(prefix)
	}
	return nil
}

const (
	defaultIngressHostPort = uint32(443)
	netproxyDNSPort        = uint32(53)
	httpsRedirectHostPort  = uint32(80)
)

func validateIngress(ingress []apigen.Ingress, secretStore SecretResolver) error {
	seen := map[ingressRouteKey]bool{}
	seenHTTPS := map[httpsRouteKey]bool{}
	for i := range ingress {
		route := &ingress[i]
		hostname, ok := ingressHostname(route.Hostname)
		if !ok {
			return InvalidConfigErrf("networking.ingress.hostname must be a valid DNS hostname")
		}
		route.Hostname = hostname
		if err := ValidateIngressListen(route.Listen); err != nil {
			return err
		}
		switch config := route.Config.Value; {
		case config.TlsPassthrough != nil && config.Https != nil:
			return InvalidConfigErrf("networking.ingress.config: exactly one of tlsPassthrough or https must be set")
		case config.TlsPassthrough != nil:
			cfg := config.TlsPassthrough
			if cfg.HostPort.Present && (cfg.HostPort.Value < 1 || cfg.HostPort.Value > 65535) {
				return InvalidConfigErrf("networking.ingress.tlsPassthroughConfig.hostPort must be between 1 and 65535 when set")
			}
			if cfg.ContainerPort < 1 || cfg.ContainerPort > 65535 {
				return InvalidConfigErrf("networking.ingress.tlsPassthroughConfig.containerPort must be between 1 and 65535")
			}
			hostPort := ingressHostPort(cfg.HostPort)
			if hostPort == netproxyDNSPort {
				return InvalidConfigErrf("networking.ingress.tlsPassthroughConfig.hostPort %d is reserved for opendeploy-net DNS", hostPort)
			}
			if hostPort == httpsRedirectHostPort {
				return InvalidConfigErrf("networking.ingress.tlsPassthroughConfig.hostPort %d is reserved for HTTPS ingress redirects", hostPort)
			}
			key := ingressRouteKey{hostPort: hostPort, hostname: hostname}
			if seen[key] {
				return InvalidConfigErrf("networking.ingress: duplicate TLS_PASSTHROUGH route for %s on host port %d", hostname, key.hostPort)
			}
			seen[key] = true
		case config.Https != nil:
			if err := validateHTTPSConfig(config.Https, hostname, secretStore); err != nil {
				return err
			}
			key := httpsRouteKey{hostname: hostname, pathPrefix: config.Https.PathPrefix}
			if seenHTTPS[key] {
				return InvalidConfigErrf("networking.ingress: duplicate HTTPS route for %s%s", hostname, key.pathPrefix)
			}
			seenHTTPS[key] = true
		default:
			return InvalidConfigErrf("networking.ingress.config: exactly one of tlsPassthrough or https must be set")
		}
	}
	for key := range seenHTTPS {
		if seen[ingressRouteKey{hostPort: defaultIngressHostPort, hostname: key.hostname}] {
			return InvalidConfigErrf("networking.ingress: %s cannot use both HTTPS and TLS_PASSTHROUGH on host port %d", key.hostname, defaultIngressHostPort)
		}
	}
	return nil
}

// ValidateIngressListen checks each listen selector's shape and canonicalises
// its address prefixes in place: a prefix is masked to its length, and the
// same prefix may appear only once per selector.
func ValidateIngressListen(entries []apigen.IngressListen) error {
	for i := range entries {
		entry := &entries[i]
		if entry.Node.Present {
			node := entry.Node.Value.Value
			if node.Any == nil && node.Specific == nil {
				return InvalidConfigErrf("networking.ingress.listen.node: any or specific is required")
			}
			if node.Specific != nil && node.Specific.NodeID == 0 {
				return InvalidConfigErrf("networking.ingress.listen.node.nodeId must be positive")
			}
		}
		seen := map[netip.Prefix]bool{}
		for j := range entry.Addresses {
			prefix := entry.Addresses[j].Prefix()
			if !prefix.IsValid() {
				return InvalidConfigErrf("networking.ingress.listen.address: entry is not an IP address or CIDR prefix")
			}
			if prefix.Addr().Is4In6() {
				return InvalidConfigErrf("networking.ingress.listen.address: %q must be a plain IPv4 or IPv6 prefix", prefix)
			}
			canonical := prefix.Masked()
			if seen[canonical] {
				return InvalidConfigErrf("networking.ingress.listen.address: duplicate entry %q", network.FilterEntryString(canonical))
			}
			seen[canonical] = true
			entry.Addresses[j] = apigen.PrefixOf(canonical)
		}
	}
	return nil
}

type certSecretRevealer interface {
	RevealByRef(ref apigen.ValueRef) ([]byte, error)
}

func validateHTTPSConfig(cfg *apigen.HttpsConfig, hostname string, secretStore SecretResolver) error {
	if cfg.ContainerPort < 1 || cfg.ContainerPort > 65535 {
		return InvalidConfigErrf("networking.ingress.httpsConfig.containerPort must be between 1 and 65535")
	}
	prefix, ok := normalizeHTTPSPathPrefix(cfg.PathPrefix)
	if !ok {
		return InvalidConfigErrf("networking.ingress.httpsConfig.pathPrefix must be a clean absolute path")
	}
	cfg.PathPrefix = prefix
	switch cfg.BackendProtocol {
	case apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_UNSPECIFIED, apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_H2C, apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_HTTP1:
	default:
		return InvalidConfigErrf("networking.ingress.httpsConfig.backendProtocol: unsupported value %d", cfg.BackendProtocol)
	}
	if cfg.MaxRequestBodyBytes.Present && cfg.MaxRequestBodyBytes.Value == 0 {
		return InvalidConfigErrf("networking.ingress.httpsConfig.maxRequestBodyBytes must be at least 1 when set")
	}
	if !cfg.CertSource.Present {
		return nil
	}
	source := cfg.CertSource.Value.Value
	hasAcme := source.Acme != nil
	hasSecret := source.Secret != nil
	if hasAcme == hasSecret {
		return InvalidConfigErrf("networking.ingress.httpsConfig.certSource: exactly one of acme or secret must be set")
	}
	if hasAcme {
		switch source.Acme.Challenge {
		case apigen.AcmeChallenge_ACME_CHALLENGE_UNSPECIFIED, apigen.AcmeChallenge_ACME_CHALLENGE_HTTP_01:
		default:
			return InvalidConfigErrf("networking.ingress.httpsConfig.certSource.acme.challenge: unsupported value %d", source.Acme.Challenge)
		}
	}
	if hasSecret {
		ref := source.Secret.Secret.Ref()
		if !ref.Valid() {
			return InvalidConfigErrf("networking.ingress.httpsConfig.certSource.secret.secret: id and version must be positive")
		}
		if secretStore == nil {
			return InvalidConfigErrf("networking.ingress.httpsConfig.certSource.secret: secrets cannot be resolved here")
		}
		if _, ok := secretStore.MetaByRef(ref); !ok {
			return InvalidConfigErrf("networking.ingress.httpsConfig.certSource.secret: unknown secret %s", ref)
		}
		if revealer, ok := secretStore.(certSecretRevealer); ok {
			if err := validateCertSecret(revealer, ref, hostname); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateCertSecret(revealer certSecretRevealer, ref apigen.ValueRef, hostname string) error {
	value, err := revealer.RevealByRef(ref)
	if err != nil {
		return InvalidConfigErrf("networking.ingress.httpsConfig.certSource.secret: reading secret %s failed", ref)
	}
	pair, err := tls.X509KeyPair(value, value)
	if err != nil {
		return InvalidConfigErrf("networking.ingress.httpsConfig.certSource.secret: secret %s must hold a combined PEM certificate and private key: %v", ref, err)
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return InvalidConfigErrf("networking.ingress.httpsConfig.certSource.secret: parsing certificate in secret %s failed: %v", ref, err)
	}
	if err := leaf.VerifyHostname(hostname); err != nil {
		return InvalidConfigErrf("networking.ingress.httpsConfig.certSource.secret: certificate in secret %s does not cover %s", ref, hostname)
	}
	if time.Now().After(leaf.NotAfter) {
		return InvalidConfigErrf("networking.ingress.httpsConfig.certSource.secret: certificate in secret %s expired on %s", ref, leaf.NotAfter.Format(time.RFC3339))
	}
	return nil
}

func normalizeHTTPSPathPrefix(prefix string) (string, bool) {
	prefix = strings.TrimSpace(prefix)
	if prefix == "" || prefix == "/" {
		return "/", true
	}
	if !strings.HasPrefix(prefix, "/") || strings.ContainsAny(prefix, "?#\\ \t") {
		return "", false
	}
	prefix = strings.TrimSuffix(prefix, "/")
	if path.Clean(prefix) != prefix {
		return "", false
	}
	for _, char := range prefix {
		if char <= 0x20 || char == 0x7f {
			return "", false
		}
	}
	return prefix, true
}

type httpsRouteKey struct {
	hostname   string
	pathPrefix string
}

type ingressRouteKey struct {
	hostPort uint32
	hostname string
}

func ingressHostPort(port apigen.Maybe[uint32]) uint32 {
	if !port.Present || port.Value == 0 {
		return defaultIngressHostPort
	}
	return port.Value
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

type portForwardKey struct {
	protocol apigen.PortForwardProtocol
	hostPort uint32
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

func validateRuntimeEnvRefs(spec *apigen.DeploymentSpec, secretStore SecretResolver, configs ConfigResolver) error {
	if spec == nil || spec.Container() == nil || len(spec.Container().Runtime.EnvVars) == 0 {
		return nil
	}
	for _, value := range spec.Container().Runtime.EnvVars {
		if secret := value.Value.Secret; secret != nil {
			if secretStore == nil {
				return InvalidConfigErrf("container1Spec.runtime.envVars: secrets cannot be resolved here")
			}
			if _, ok := secretStore.MetaByRef(secret.Secret.Ref()); !ok {
				return InvalidConfigErrf("container1Spec.runtime.envVars: unknown secret %s", secret.Secret)
			}
		}
		if config := value.Value.Config; config != nil {
			if configs == nil {
				return InvalidConfigErrf("container1Spec.runtime.envVars: configs cannot be resolved here")
			}
			if _, ok := configs.ResolveConfig(config.Config.Ref()); !ok {
				return InvalidConfigErrf("container1Spec.runtime.envVars: unknown config %s", config.Config)
			}
		}
	}
	return nil
}

func validateAddressEnvRefs(live nodes.LiveState, nodeID, deploymentID, spaceID uint64, spec *apigen.DeploymentSpec) error {
	if spec == nil || spec.Container() == nil {
		return nil
	}
	configs := live.Deployments
	for key, value := range spec.Container().Runtime.EnvVars {
		address := value.Value.Address
		if address == nil {
			continue
		}
		targetID := address.DeploymentID
		if targetID == deploymentID && deploymentID != 0 {
			return InvalidConfigErrf("container1Spec.runtime.envVars.%s: deployment cannot reference its own address", key)
		}
		target := configs[targetID]
		if target == nil {
			return InvalidConfigErrf("container1Spec.runtime.envVars.%s: unknown address deployment id %d", key, targetID)
		}
		if target.Deployment.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			return InvalidConfigErrf("container1Spec.runtime.envVars.%s: address deployment must use virtual networking", key)
		}
		if target.Deployment.SpaceID != address.SpaceID {
			return InvalidConfigErrf("container1Spec.runtime.envVars.%s: address space does not match deployment", key)
		}
		if target.Deployment.SpaceID != spaceID && target.Deployment.SpaceID != nodes.DefaultSpaceID {
			return InvalidConfigErrf("container1Spec.runtime.envVars.%s: address deployment %q lives in space %d and cannot be referenced from a deployment in space %d", key, target.Deployment.Name, target.Deployment.SpaceID, spaceID)
		}
	}
	return nil
}

func validateContainerSource(source *apigen.ContainerSource) error {
	if source == nil {
		return InvalidConfigErrf("container1Spec.source is required")
	}
	nix, remote := source.Value.NixImageBuild, source.Value.RemoteImage
	hasNixDocker := nix != nil
	hasRemoteImage := remote != nil
	if hasNixDocker == hasRemoteImage {
		return InvalidConfigErrf("container1Spec.source: exactly one of nixDockerBuild or remoteImage must be set")
	}
	if hasNixDocker {
		if nix.Repo == "" {
			return InvalidConfigErrf("container1Spec.source.nixDockerBuild: repo is required")
		}
		if nix.Flake == "" {
			return InvalidConfigErrf("container1Spec.source.nixDockerBuild: flake is required")
		}
		flakePath, err := gitrepo.CleanFlakePath(nix.Flake)
		if err != nil {
			return InvalidConfigErrf("container1Spec.source.nixDockerBuild.flake: %v", err)
		}
		nix.Flake = flakePath
		target := nix.Target
		if target != "" && (target != strings.TrimSpace(target) || !strings.HasPrefix(target, ".#")) {
			return InvalidConfigErrf("container1Spec.source.nixDockerBuild.target: must be a local flake selector starting with .#")
		}
	}
	if hasRemoteImage {
		if remote.Image == "" {
			return InvalidConfigErrf("container1Spec.source.remoteImage: image is required")
		}
		if remote.Image == internaldeploy.NetproxyImage {
			return InvalidConfigErrf("container1Spec.source.remoteImage: opendeploy-net image is internal-only")
		}
	}
	return nil
}

func validateNixWorkloadVersion(def *apigen.Deployment) error {
	spec := &def.Spec
	if nixSource(spec) == nil {
		return nil
	}
	version := spec.WorkloadVersion()
	if version == "" {
		if def.Running() {
			return InvalidConfigErrf("container1Spec.version is required for a running Nix deployment")
		}
		return nil
	}
	if err := gitrepo.ValidateFullCommitHash(version); err != nil {
		return InvalidConfigErrf("container1Spec.version: %v", err)
	}
	return nil
}

func verifyRunningNixSource(gitVersions NixSourceVerifier, ctx apigen.Context, spec *apigen.DeploymentSpec) error {
	nix := nixSource(spec)
	if nix == nil {
		return nil
	}
	if gitVersions == nil {
		return apigen.NewApiErr("Nix source verification is not configured", "nix_source_verification_unavailable", http.StatusServiceUnavailable)
	}
	if _, err := gitVersions.ValidateNixSource(ctx, nix.Repo, spec.WorkloadVersion(), nix.Flake); err != nil {
		return apigen.NewApiErr(fmt.Sprintf("Nix source verification failed: %v", err), "nix_source_verification_failed", http.StatusBadRequest)
	}
	return nil
}

func nixSource(spec *apigen.DeploymentSpec) *apigen.NixImageBuild {
	if spec == nil || spec.Container() == nil {
		return nil
	}
	return spec.Container().Source.Value.NixImageBuild
}

func sameNixBuildConfig(a, b *apigen.NixImageBuild) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Repo == b.Repo && a.Flake == b.Flake && a.Target == b.Target
}

func sameDesiredVersionSource(a, b *apigen.DeploymentSpec) bool {
	if a == nil || b == nil {
		return a == b
	}
	aContainer, bContainer := a.Container(), b.Container()
	switch {
	case aContainer != nil && bContainer != nil && aContainer.Source.Value.NixImageBuild != nil && bContainer.Source.Value.NixImageBuild != nil:
		aNix, bNix := aContainer.Source.Value.NixImageBuild, bContainer.Source.Value.NixImageBuild
		aFlake, aErr := gitrepo.CleanFlakePath(aNix.Flake)
		bFlake, bErr := gitrepo.CleanFlakePath(bNix.Flake)
		if aErr != nil || bErr != nil {
			return aNix.Repo == bNix.Repo && aNix.Flake == bNix.Flake
		}
		return aNix.Repo == bNix.Repo && aFlake == bFlake
	case aContainer != nil && bContainer != nil && aContainer.Source.Value.RemoteImage != nil && bContainer.Source.Value.RemoteImage != nil:
		// A tag inside the stored reference is a version, not a source.
		aImage, bImage := aContainer.Source.Value.RemoteImage.Image, bContainer.Source.Value.RemoteImage.Image
		aRef, aErr := imageref.RepositoryRef(aImage)
		bRef, bErr := imageref.RepositoryRef(bImage)
		if aErr != nil || bErr != nil {
			return aImage == bImage
		}
		return aRef == bRef
	default:
		return false
	}
}

func validateContainerSpec(container *apigen.ContainerSpec, assets AssetResolver) error {
	if container == nil {
		return InvalidConfigErrf("container1Spec is required")
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
	if container.Runtime.IssuedTlsMount.Present {
		if err := validateIssuedTLSMount(&container.Runtime.IssuedTlsMount.Value); err != nil {
			return err
		}
	}
	return nil
}

func validateIssuedTLSMount(mount *apigen.IssuedTLSMount) error {
	path := strings.TrimSpace(mount.ContainerPath)
	if path == "" {
		return InvalidConfigErrf("container1Spec.runtime.issuedTlsMount: containerPath is required")
	}
	if !filepath.IsAbs(path) {
		return InvalidConfigErrf("container1Spec.runtime.issuedTlsMount: containerPath must be absolute")
	}
	cleanPath := filepath.Clean(path)
	if cleanPath != path || cleanPath == "/" || strings.HasSuffix(path, "/") {
		return InvalidConfigErrf("container1Spec.runtime.issuedTlsMount: containerPath must be an absolute directory path")
	}
	mount.ContainerPath = cleanPath
	if mount.CaOnly && len(mount.ExtraNames) > 0 {
		return InvalidConfigErrf("container1Spec.runtime.issuedTlsMount: extraNames are not allowed with caOnly")
	}
	if len(mount.ExtraNames) > 16 {
		return InvalidConfigErrf("container1Spec.runtime.issuedTlsMount: at most 16 extra names are allowed")
	}
	for i, name := range mount.ExtraNames {
		name = strings.TrimSpace(name)
		if name == "" || len(name) > 253 || strings.ContainsAny(name, " \t\n,*") {
			return InvalidConfigErrf("container1Spec.runtime.issuedTlsMount: extraNames[%d] is not a valid host name or IP address", i)
		}
		mount.ExtraNames[i] = name
	}
	return nil
}

func validateIssuedTLSNames(spec *apigen.DeploymentSpec, deploymentID, spaceID uint64) error {
	if spec == nil || spec.Container() == nil || !spec.Container().Runtime.IssuedTlsMount.Present {
		return nil
	}
	prefix, hasPrefix := network.Default.PrefixValue()
	for i, name := range spec.Container().Runtime.IssuedTlsMount.Value.ExtraNames {
		if err := network.ValidateIssuedName(name, int32(spaceID), int32(deploymentID), prefix, hasPrefix); err != nil {
			return InvalidConfigErrf("container1Spec.runtime.issuedTlsMount: extraNames[%d] %v", i, err)
		}
	}
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
		cfg.UpgradeStrategy = apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE
	case apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE:
		cfg.ReadinessSignal = apigen.Maybe[apigen.ContainerReadinessSignal]{}
	case apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_ROLLOVER:
		if cfg.Runtime.EnvVars != nil {
			if _, ok := cfg.Runtime.EnvVars["OPENDEPLOY_READINESS_SOCK_PATH"]; ok {
				return InvalidConfigErrf("container1Spec.runtime.envVars: OPENDEPLOY_READINESS_SOCK_PATH is reserved for rollover readiness")
			}
		}
		if !cfg.ReadinessSignal.Present {
			cfg.ReadinessSignal = apigen.Some(apigen.ContainerReadinessSignal{})
		}
		if timeout := cfg.ReadinessSignal.Value.TimeoutSeconds; timeout.Present && timeout.Value == 0 {
			return InvalidConfigErrf("container1Spec.readinessSignal.timeoutSeconds must be at least 1 when set")
		}
	default:
		return InvalidConfigErrf("container1Spec.upgradeStrategy: unsupported value %d", cfg.UpgradeStrategy)
	}
	return nil
}

func validateContainerDevShmSizeKb(cfg *apigen.ContainerRuntime) error {
	if cfg == nil || !cfg.DevShmSizeKb.Present {
		return nil
	}
	if cfg.DevShmSizeKb.Value == 0 {
		return InvalidConfigErrf("container1Spec.runtime.devShmSizeKb must be at least 1 when set")
	}
	return nil
}

func validateContainerFileDescriptorLimit(cfg *apigen.ContainerRuntime) error {
	if cfg == nil || !cfg.FileDescriptorLimit.Present {
		return nil
	}
	if cfg.FileDescriptorLimit.Value == 0 {
		return InvalidConfigErrf("container1Spec.runtime.fileDescriptorLimit must be at least 1 when set")
	}
	return nil
}

func validateDefaultVolume(mount *apigen.DefaultVolumeMount) error {
	if mount == nil || mount.ContainerPath == "" {
		return nil
	}
	path, err := cleanContainerPath(mount.ContainerPath)
	if err != nil {
		return InvalidConfigErrf("container1Spec.runtime.defaultVolume.containerPath: %v", err)
	}
	mount.ContainerPath = path
	return nil
}

func validateCustomHostMounts(mounts []apigen.HostMount) error {
	for i := range mounts {
		m := &mounts[i]
		if strings.TrimSpace(m.HostPath) == "" || strings.TrimSpace(m.ContainerPath) == "" {
			return InvalidConfigErrf("container1Spec.runtime.mounts: hostPath and containerPath are both required")
		}
		host := strings.TrimSpace(m.HostPath)
		container := strings.TrimSpace(m.ContainerPath)
		if !filepath.IsAbs(host) {
			return InvalidConfigErrf("container1Spec.runtime.mounts: hostPath must be absolute")
		}
		if filepath.Clean(host) != host || host == "/" {
			return InvalidConfigErrf("container1Spec.runtime.mounts: hostPath must be a clean absolute path")
		}
		cleaned, err := cleanContainerPath(container)
		if err != nil {
			return InvalidConfigErrf("container1Spec.runtime.mounts.containerPath: %v", err)
		}
		if containerHostMountDenied(host) {
			return InvalidConfigErrf("container1Spec.runtime.mounts: host path %q is not allowed", host)
		}
		if !validMountPermission(m.Permission) {
			return InvalidConfigErrf("container1Spec.runtime.mounts: permission is required")
		}
		m.HostPath = host
		m.ContainerPath = cleaned
	}
	return nil
}

func validateCrossDeploymentMounts(mounts []apigen.CrossDeploymentMount) error {
	for i := range mounts {
		mount := &mounts[i]
		if mount.DeploymentID == 0 {
			return InvalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: deploymentId is required")
		}
		path, err := cleanContainerPath(mount.ContainerPath)
		if err != nil {
			return InvalidConfigErrf("container1Spec.runtime.crossDeploymentMounts.containerPath: %v", err)
		}
		if !validMountPermission(mount.Permission) {
			return InvalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: permission is required")
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
	return permission == apigen.FilePermission_FILE_PERMISSION_READ_WRITE || permission == apigen.FilePermission_FILE_PERMISSION_READ_ONLY
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
	"/var/lib/opendeploy-log-archive",
	"/var/lib/opendeploy-metrics",
	"/var/lib/opendeploy-releases",
	"/var/lib/opendeploy-run-logs",
	"/var/lib/opendeploy-volumes",
}

func containerHostMountDenied(host string) bool {
	host = filepath.Clean(host)
	if host == "/" {
		return true
	}
	for _, root := range deniedContainerHostMountRoots {
		// Mounting a parent exposes the protected tree just as mounting the
		// tree itself does. This is a lexical guardrail; administrators own
		// symlinks and other filesystem aliases on the target node.
		if pathEqualOrUnder(host, root) || pathEqualOrUnder(root, host) {
			return true
		}
	}
	return false
}

func validateCrossDeploymentMountSources(live nodes.LiveState, spec *apigen.DeploymentSpec, nodeID, currentID, spaceID uint64) error {
	if spec == nil || spec.Container() == nil {
		return nil
	}
	for _, mount := range spec.Container().Runtime.CrossDeploymentMounts {
		if mount.DeploymentID == currentID && currentID != 0 {
			return InvalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: a deployment cannot mount its own default volume")
		}
		source := live.Deployments[mount.DeploymentID]
		if source == nil {
			return InvalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: source deployment %d does not exist", mount.DeploymentID)
		}
		if source.Deployment.PlacementNodeID() != nodeID {
			return InvalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: source deployment %d is on a different node", mount.DeploymentID)
		}
		if source.Deployment.SpaceID != spaceID && source.Deployment.SpaceID != nodes.DefaultSpaceID {
			return InvalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: source deployment %q lives in space %d and cannot be mounted from a deployment in space %d", source.Deployment.Name, source.Deployment.SpaceID, spaceID)
		}
		container := source.Deployment.Spec.Container()
		if container == nil || container.Runtime.DefaultVolume.Disabled {
			return InvalidConfigErrf("container1Spec.runtime.crossDeploymentMounts: source deployment %d has no default volume", mount.DeploymentID)
		}
	}
	return nil
}

func pathEqualOrUnder(path, root string) bool {
	root = filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+string(filepath.Separator))
}

func resolveAssetMounts(in []apigen.AssetMount, assets AssetResolver) ([]apigen.AssetMount, error) {
	if len(in) == 0 {
		return nil, nil
	}
	if assets == nil {
		return nil, InvalidConfigErrf("container1Spec.runtime.assetMounts: assets cannot be resolved here")
	}
	out := make([]apigen.AssetMount, 0, len(in))
	for _, m := range in {
		path := strings.TrimSpace(m.ContainerPath)
		if !m.Asset.Valid() || path == "" {
			return nil, InvalidConfigErrf("container1Spec.runtime.assetMounts: asset and path are both required")
		}
		if !filepath.IsAbs(path) {
			return nil, InvalidConfigErrf("container1Spec.runtime.assetMounts: path must be absolute")
		}
		cleanPath := filepath.Clean(path)
		if cleanPath != path || cleanPath == "/" || strings.HasSuffix(path, "/") {
			return nil, InvalidConfigErrf("container1Spec.runtime.assetMounts: path must be an absolute file path")
		}
		asset, ok := assets.GetAssetVersionRef(m.Asset.Ref())
		if !ok {
			return nil, InvalidConfigErrf("container1Spec.runtime.assetMounts: asset %s not found", m.Asset)
		}
		if m.Permission != apigen.FilePermission_FILE_PERMISSION_READ_ONLY && m.Permission != apigen.FilePermission_FILE_PERMISSION_READ_EXECUTE {
			return nil, InvalidConfigErrf("container1Spec.runtime.assetMounts: permission must be READ_ONLY or READ_EXECUTE")
		}
		out = append(out, apigen.AssetMount{Asset: asset.Ref.Asset(), ContainerPath: cleanPath, Permission: m.Permission})
	}
	return out, nil
}

func resolveEnvAssetRefs(scope string, env map[string]apigen.EnvVar, assets AssetResolver) error {
	for key, value := range env {
		if value.Value.Asset == nil {
			continue
		}
		if assets == nil {
			return InvalidConfigErrf("%s.%s: assets cannot be resolved here", scope, key)
		}
		asset, ok := assets.GetAssetVersionRef(value.Value.Asset.Asset.Ref())
		if !ok {
			return InvalidConfigErrf("%s.%s: asset %s not found", scope, key, value.Value.Asset.Asset)
		}
		value.Value.Asset.Key = asset.Key
	}
	return nil
}

// validateEnvVars trims and validates env keys and typed values. Duplicate keys
// after trimming are rejected so the resulting process environment is unambiguous.
func validateEnvVars(scope string, in map[string]apigen.EnvVar) error {
	seen := make(map[string]struct{}, len(in))
	out := make(map[string]apigen.EnvVar, len(in))
	for rawKey, value := range in {
		key := strings.TrimSpace(rawKey)
		if key == "" {
			return InvalidConfigErrf("%s: key is required", scope)
		}
		if _, dup := seen[key]; dup {
			return InvalidConfigErrf("%s: duplicate key %q", scope, key)
		}
		seen[key] = struct{}{}
		set := 0
		if value.Value.Literal != nil {
			set++
		}
		if secret := value.Value.Secret; secret != nil {
			set++
			if !secret.Secret.Valid() {
				return InvalidConfigErrf("%s.%s: secret id and version must be positive", scope, key)
			}
		}
		if config := value.Value.Config; config != nil {
			set++
			if !config.Config.Valid() {
				return InvalidConfigErrf("%s.%s: config id and version must be positive", scope, key)
			}
		}
		if asset := value.Value.Asset; asset != nil {
			set++
			if !asset.Asset.Valid() {
				return InvalidConfigErrf("%s.%s: asset id and version must be positive", scope, key)
			}
		}
		if address := value.Value.Address; address != nil {
			set++
			if address.DeploymentID == 0 {
				return InvalidConfigErrf("%s.%s: addressDeploymentId must be positive", scope, key)
			}
			if address.DeploymentID > uint64(network.MaxDeploymentID) {
				return InvalidConfigErrf("%s.%s: addressDeploymentId must not exceed %d", scope, key, network.MaxDeploymentID)
			}
			if address.SpaceID > uint64(network.MaxSpaceID) {
				return InvalidConfigErrf("%s.%s: addressSpaceId must be between 0 and %d", scope, key, network.MaxSpaceID)
			}
		}
		if set != 1 {
			return InvalidConfigErrf("%s.%s: exactly one of literal, secret, config, asset, or address is required", scope, key)
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

func InvalidConfigErrf(format string, args ...any) error {
	e := InvalidConfigErr
	msg := fmt.Sprintf(format, args...)
	e.InternalErr = msg
	e.DisplayErr = msg
	return e
}
