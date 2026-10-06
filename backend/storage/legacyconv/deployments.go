package legacyconv

import (
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
)

// Deployment converts a deployment payload, including the pinned record inside
// a ScheduledInstanceState.
func Deployment(old *apigenold.Deployment) (*apigen.Deployment, error) {
	c := &conv{}
	if old == nil {
		c.refuse("Deployment", "", "nil payload")
		return checked("Deployment", (*apigen.Deployment)(nil), c.err)
	}
	return checked("Deployment", c.deployment(old), c.err)
}

func (c *conv) deployment(old *apigenold.Deployment) *apigen.Deployment {
	return &apigen.Deployment{
		ID:         c.id("Deployment", "id", int64(old.ID)),
		Name:       old.Name,
		SpaceID:    c.id("Deployment", "space_id", int64(old.SpaceID)),
		Spec:       c.deploymentSpec(&old.Spec),
		Scheduling: c.scheduling(&old.Scheduling),
	}
}

func (c *conv) scheduling(old *apigenold.Scheduling) apigen.Scheduling {
	out := apigen.Scheduling{Running: old.Running, RestartGeneration: c.u32("Scheduling", "generation", int64(old.Generation))}
	if old.DedicatedNodes == nil {
		c.refuse("Scheduling", "dedicated_nodes", "unset; the placement union has no alternative for an unplaced deployment")
		return out
	}
	out.Placement = apigen.Placement{Value: apigen.PlacementValueOneof{DedicatedNodes: &apigen.DedicatedNodesScheduling{
		Nodes: c.ids("DedicatedNodesScheduling", "nodes", old.DedicatedNodes.Nodes),
	}}}
	return out
}

func (c *conv) deploymentSpec(old *apigenold.DeploymentSpec) apigen.DeploymentSpec {
	if old.Container2Spec != nil || old.Container3Spec != nil {
		c.refuse("DeploymentSpec", "container2_spec", "container2_spec and container3_spec have no alternative in the workload union")
		return apigen.DeploymentSpec{}
	}
	if old.OpendeploySpec != nil {
		if old.Container1Spec != nil {
			c.refuse("DeploymentSpec", "opendeploy_spec", "set beside container1_spec")
			return apigen.DeploymentSpec{}
		}
		if len(old.Networking.PortForwarding) > 0 || len(old.Networking.Ingress) > 0 {
			c.refuse("DeploymentSpec", "networking", "an opendeploy_spec deployment with port forwards or ingress has no self-spec form")
			return apigen.DeploymentSpec{}
		}
		spec := internaldeploy.SelfSpec()
		spec.Workload.Value.Container.Version = old.OpendeploySpec.Version
		return *spec
	}
	if old.Container1Spec == nil {
		c.refuse("DeploymentSpec", "container1_spec", "no workload is set")
		return apigen.DeploymentSpec{}
	}
	return apigen.DeploymentSpec{
		Workload:   apigen.Workload{Value: apigen.WorkloadValueOneof{Container: c.containerSpec(old.Container1Spec)}},
		Networking: c.networking(&old.Networking),
	}
}

func (c *conv) containerSpec(old *apigenold.ContainerSpec) *apigen.ContainerSpec {
	out := &apigen.ContainerSpec{
		Source:          c.containerSource(&old.Source),
		Runtime:         c.containerRuntime(&old.Runtime),
		Version:         old.Version,
		UpgradeStrategy: apigen.ContainerUpgradeStrategy(old.UpgradeStrategy),
	}
	if out.UpgradeStrategy == apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_UNSPECIFIED {
		out.UpgradeStrategy = apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE
	}
	if old.ReadinessSignal != nil {
		out.ReadinessSignal = apigen.Some(apigen.ContainerReadinessSignal{TimeoutSeconds: c.optU32("ContainerReadinessSignal", "timeout_seconds", old.ReadinessSignal.TimeoutSeconds)})
	}
	return out
}

func (c *conv) containerSource(old *apigenold.ContainerBundleSource) apigen.ContainerSource {
	switch {
	case old.NixDockerBuild != nil && old.RemoteImage != nil:
		c.refuse("ContainerBundleSource", "", "both nix_docker_build and remote_image are set")
	case old.NixDockerBuild != nil:
		return apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{NixImageBuild: &apigen.NixImageBuild{
			Repo: old.NixDockerBuild.Repo, Flake: old.NixDockerBuild.Flake, Target: old.NixDockerBuild.Target,
		}}}
	case old.RemoteImage != nil:
		return apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: old.RemoteImage.Image}}}
	default:
		c.refuse("ContainerBundleSource", "", "neither nix_docker_build nor remote_image is set")
	}
	return apigen.ContainerSource{}
}

func (c *conv) containerRuntime(old *apigenold.ContainerRuntime) apigen.ContainerRuntime {
	out := apigen.ContainerRuntime{
		User:                old.User,
		OverrideCommand:     old.OverrideCommand,
		OverrideWorkingDir:  old.OverrideWorkingDir,
		DefaultVolume:       apigen.DefaultVolumeMount{ContainerPath: old.DefaultVolume.ContainerPath, Disabled: old.DefaultVolume.Disabled},
		DevShmSizeKb:        c.optU32("ContainerRuntime", "dev_shm_size_kb", old.DevShmSizeKb),
		FileDescriptorLimit: c.optU32("ContainerRuntime", "file_descriptor_limit", old.FileDescriptorLimit),
	}
	if len(old.EnvVars) > 0 {
		out.EnvVars = make(map[string]apigen.EnvVar, len(old.EnvVars))
		for k, v := range old.EnvVars {
			if v == nil {
				c.refuse("ContainerRuntime", "env_vars", "%q has no value", k)
				continue
			}
			if ev, keep := c.envVar(k, v); keep {
				out.EnvVars[k] = ev
			}
		}
	}
	for _, m := range old.CrossDeploymentMounts {
		if m == nil {
			c.refuse("ContainerRuntime", "cross_deployment_mounts", "nil entry")
			continue
		}
		out.CrossDeploymentMounts = append(out.CrossDeploymentMounts, apigen.CrossDeploymentMount{
			DeploymentID: c.id("CrossDeploymentMount", "deployment_id", int64(m.DeploymentID)), ContainerPath: m.ContainerPath, Permission: apigen.FilePermission(m.Permission),
		})
	}
	for _, m := range old.Mounts {
		if m == nil {
			c.refuse("ContainerRuntime", "mounts", "nil entry")
			continue
		}
		out.Mounts = append(out.Mounts, apigen.HostMount{HostPath: m.HostPath, ContainerPath: m.ContainerPath, Permission: apigen.FilePermission(m.Permission)})
	}
	for _, m := range old.AssetMounts {
		if m == nil {
			c.refuse("ContainerRuntime", "asset_mounts", "nil entry")
			continue
		}
		out.AssetMounts = append(out.AssetMounts, apigen.AssetMount{
			Asset: c.assetRef("AssetMount", "asset", &m.Asset), ContainerPath: m.ContainerPath, Permission: apigen.FilePermission(m.Permission),
		})
	}
	if old.IssuedTlsMount != nil {
		out.IssuedTlsMount = apigen.Some(apigen.IssuedTLSMount{
			ContainerPath: old.IssuedTlsMount.ContainerPath, ExtraNames: old.IssuedTlsMount.ExtraNames, CaOnly: old.IssuedTlsMount.CaOnly,
		})
	}
	return out
}

// envVar reports false when a historic row's env var is dropped: one that
// carries only an asset display key from before value references became pairs.
// A display key beside a literal is the literal: the pairs rewrite left
// "<unknown ref>" literals with the key as an annotation, and the old
// validator read the value.
func (c *conv) envVar(key string, old *apigenold.EnvVarValue) (apigen.EnvVar, bool) {
	typ := "EnvVarValue[" + key + "]"
	var v apigen.EnvVarValueOneof
	alternatives := 0
	if old.Value != nil {
		alternatives++
		v.Literal = &apigen.LiteralEnv{Value: *old.Value}
	}
	if old.Secret != nil {
		alternatives++
		v.Secret = &apigen.SecretEnv{Secret: c.secretRef(typ, "secret", old.Secret)}
	}
	if old.Config != nil {
		alternatives++
		v.Config = &apigen.ConfigEnv{Config: c.configRef(typ, "config", old.Config)}
	}
	if old.AssetRef != nil {
		alternatives++
		v.Asset = &apigen.AssetEnv{Key: old.Asset, Asset: c.assetRef(typ, "asset_ref", old.AssetRef)}
	} else if old.Asset != "" && old.Value == nil {
		onlyKey := old.Secret == nil && old.Config == nil && old.AddressDeploymentID == nil && old.AddressSpaceID == nil
		if c.historic && onlyKey {
			c.repair(typ, "asset", "dropped, display key %q without an asset_ref", old.Asset)
			return apigen.EnvVar{}, false
		}
		c.refuse(typ, "asset", "display key %q without an asset_ref", old.Asset)
	}
	if old.AddressDeploymentID != nil || old.AddressSpaceID != nil {
		alternatives++
		if old.AddressDeploymentID == nil || old.AddressSpaceID == nil {
			c.refuse(typ, "address_deployment_id", "address_deployment_id and address_space_id must be set together")
		} else {
			v.Address = &apigen.AddressEnv{
				DeploymentID: c.id(typ, "address_deployment_id", int64(*old.AddressDeploymentID)),
				SpaceID:      c.id(typ, "address_space_id", int64(*old.AddressSpaceID)),
			}
		}
	}
	if alternatives != 1 {
		c.refuse(typ, "", "%d alternatives set, the env var union takes exactly one", alternatives)
	}
	return apigen.EnvVar{Value: v}, true
}

func (c *conv) networking(old *apigenold.NetworkingConfig) apigen.NetworkingConfig {
	out := apigen.NetworkingConfig{Mode: apigen.NetworkingMode(old.Mode)}
	if out.Mode == apigen.NetworkingMode_NETWORKING_MODE_UNSPECIFIED {
		// The runner treated every mode but VIRTUAL as host networking, so a
		// saved spec without a mode ran on the host.
		out.Mode = apigen.NetworkingMode_NETWORKING_MODE_HOST
	}
	for _, pf := range old.PortForwarding {
		if pf == nil {
			c.refuse("NetworkingConfig", "port_forwarding", "nil entry")
			continue
		}
		out.PortForwarding = append(out.PortForwarding, apigen.PortForward{
			Protocol: apigen.PortForwardProtocol(pf.Protocol), HostPort: c.u32("PortForward", "host_port", int64(pf.HostPort)), ContainerPort: c.u32("PortForward", "container_port", int64(pf.ContainerPort)), IpFilter: c.ipFilter(pf.IpFilter),
		})
	}
	for _, in := range old.Ingress {
		if in == nil {
			c.refuse("NetworkingConfig", "ingress", "nil entry")
			continue
		}
		out.Ingress = append(out.Ingress, c.ingress(in))
	}
	return out
}

func (c *conv) ipFilter(old *apigenold.IpFilter) []apigen.IpFilter {
	if old == nil {
		return nil
	}
	var out []apigen.IpFilter
	for _, s := range old.Allow {
		out = append(out, apigen.IpFilter{Mode: apigen.IpFilterMode_IP_FILTER_MODE_ALLOW, Prefix: c.prefix("IpFilter", "allow", s)})
	}
	for _, s := range old.Deny {
		out = append(out, apigen.IpFilter{Mode: apigen.IpFilterMode_IP_FILTER_MODE_DENY, Prefix: c.prefix("IpFilter", "deny", s)})
	}
	return out
}

func (c *conv) ingress(old *apigenold.Ingress) apigen.Ingress {
	out := apigen.Ingress{Hostname: old.Hostname}
	for _, l := range old.Listen {
		if l == nil {
			c.refuse("Ingress", "listen", "nil entry")
			continue
		}
		out.Listen = append(out.Listen, c.ingressListen(l))
	}
	switch old.Kind {
	case apigenold.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH:
		if old.TlsPassthroughConfig == nil {
			c.refuse("Ingress", "tls_passthrough_config", "unset for a TLS passthrough ingress")
			break
		}
		out.Config = apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{TlsPassthrough: &apigen.TlsPassthroughConfig{
			HostPort: c.optU32("TlsPassthroughConfig", "host_port", old.TlsPassthroughConfig.HostPort), ContainerPort: c.u32("TlsPassthroughConfig", "container_port", int64(old.TlsPassthroughConfig.ContainerPort)),
		}}}
	case apigenold.IngressKind_INGRESS_KIND_HTTPS:
		if old.HttpsConfig == nil {
			c.refuse("Ingress", "https_config", "unset for an HTTPS ingress")
			break
		}
		out.Config = apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{Https: c.httpsConfig(old.HttpsConfig)}}
	default:
		c.refuse("Ingress", "kind", "unsupported value %d", old.Kind)
	}
	return out
}

func (c *conv) httpsConfig(old *apigenold.HttpsConfig) *apigen.HttpsConfig {
	out := &apigen.HttpsConfig{
		ContainerPort:       c.u32("HttpsConfig", "container_port", int64(old.ContainerPort)),
		PathPrefix:          old.PathPrefix,
		StripPrefix:         old.StripPrefix,
		BackendProtocol:     apigen.HttpBackendProtocol(old.BackendProtocol),
		MaxRequestBodyBytes: c.optU64("HttpsConfig", "max_request_body_bytes", old.MaxRequestBodyBytes),
		FlushIntervalMs:     c.optU32("HttpsConfig", "flush_interval_ms", old.FlushIntervalMs),
	}
	if out.BackendProtocol == apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_UNSPECIFIED {
		out.BackendProtocol = apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_HTTP1
	}
	if old.CertSource != nil {
		out.CertSource = apigen.Some(c.certSource(old.CertSource))
	}
	return out
}

func (c *conv) certSource(old *apigenold.CertSource) apigen.CertSource {
	switch {
	case old.Acme != nil && old.Secret != nil:
		c.refuse("CertSource", "", "both acme and secret are set")
	case old.Acme != nil:
		challenge := apigen.AcmeChallenge(old.Acme.Challenge)
		if challenge == apigen.AcmeChallenge_ACME_CHALLENGE_UNSPECIFIED {
			challenge = apigen.AcmeChallenge_ACME_CHALLENGE_HTTP_01
		}
		return apigen.CertSource{Value: apigen.CertSourceValueOneof{Acme: &apigen.AcmeCertSource{Challenge: challenge}}}
	case old.Secret != nil:
		return apigen.CertSource{Value: apigen.CertSourceValueOneof{Secret: &apigen.SecretCertSource{Secret: c.secretRef("SecretCertSource", "secret", &old.Secret.Secret)}}}
	default:
		c.refuse("CertSource", "", "neither acme nor secret is set")
	}
	return apigen.CertSource{}
}

func (c *conv) ingressListen(old *apigenold.IngressListen) apigen.IngressListen {
	var out apigen.IngressListen
	if n := old.Node; n != nil {
		switch {
		case n.Any && n.NodeID != 0:
			c.refuse("NodeSelector", "", "any and node_id are both set")
		case n.Any:
			out.Node = apigen.Some(apigen.IngressNode{Value: apigen.IngressNodeValueOneof{Any: &apigen.AnyNode{}}})
		case n.NodeID != 0:
			out.Node = apigen.Some(apigen.IngressNode{Value: apigen.IngressNodeValueOneof{Specific: &apigen.SpecificNode{NodeID: c.id("NodeSelector", "node_id", int64(n.NodeID))}}})
		}
	}
	if a := old.Address; a != nil {
		for _, s := range a.Prefixes {
			out.Addresses = append(out.Addresses, c.prefix("AddressSelector", "prefixes", s))
		}
		if len(a.Prefixes) == 0 {
			switch a.Family {
			case apigenold.AddressFamily_ADDRESS_FAMILY_ANY:
			case apigenold.AddressFamily_ADDRESS_FAMILY_IPV4:
				out.Addresses = []apigen.IpPrefix{c.prefix("AddressSelector", "family", "0.0.0.0/0")}
			case apigenold.AddressFamily_ADDRESS_FAMILY_IPV6:
				out.Addresses = []apigen.IpPrefix{c.prefix("AddressSelector", "family", "::/0")}
			default:
				c.refuse("AddressSelector", "family", "unsupported value %d", a.Family)
			}
		}
	}
	return out
}
