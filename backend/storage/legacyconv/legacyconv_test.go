package legacyconv

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
	"github.com/jptrs93/opsagent/backend/lib/engine/internaldeploy"
)

type encoder interface {
	Encode() []byte
	EncodeChecked() ([]byte, error)
}

func assertSame(t *testing.T, got, want encoder) {
	t.Helper()
	w, err := want.EncodeChecked()
	if err != nil {
		t.Fatalf("expected value is invalid: %v", err)
	}
	if !bytes.Equal(got.Encode(), w) {
		g, _ := json.MarshalIndent(got, "", "  ")
		wj, _ := json.MarshalIndent(want, "", "  ")
		t.Fatalf("converted value differs\n got: %s\nwant: %s", g, wj)
	}
}

func ms(v int64) time.Time { return time.UnixMilli(v) }

func i32p(v int32) *int32 { return &v }

func pfx(s string) apigen.IpPrefix {
	p, err := apigen.ParsePrefix(s)
	if err != nil {
		panic(err)
	}
	return p
}

func addr(s string) apigen.IpAddress {
	a, err := apigen.ParseAddr(s)
	if err != nil {
		panic(err)
	}
	return a
}

func vref(id, version int32) *apigenold.ValueRef {
	return &apigenold.ValueRef{ID: id, Version: version}
}

func sel(wildcard bool, argument int64, include, exclude []int64) *apigenold.AuthzSelector {
	return &apigenold.AuthzSelector{Wildcard: wildcard, ArgumentID: argument, Include: include, Exclude: exclude}
}

func allow(delegation bool) apigen.AuthzEffect {
	return apigen.AuthzEffect{Value: apigen.AuthzEffectValueOneof{Allow: &apigen.AuthzAllow{DelegationAllowed: delegation}}}
}

func verbsExcluding(vs ...apigen.AuthzVerb) apigen.AuthzPermissionSelector {
	return apigen.AuthzPermissionSelector{AllVerbsExcluding: apigen.Some(apigen.AuthzVerbList{Values: vs})}
}

func exactVerbs(vs ...apigen.AuthzVerb) apigen.AuthzPermissionSelector {
	return apigen.AuthzPermissionSelector{ExactVerbs: apigen.Some(apigen.AuthzVerbList{Values: vs})}
}

func spacesExcluding(ids ...uint64) apigen.AuthzSpaceSelector {
	return apigen.AuthzSpaceSelector{AllSpacesExcluding: apigen.Some(apigen.SpaceIdList{Values: ids})}
}

func exactSpaces(ids ...uint64) apigen.AuthzSpaceSelector {
	return apigen.AuthzSpaceSelector{ExactSpaces: apigen.Some(apigen.SpaceIdList{Values: ids})}
}

func typesExcluding(ks ...apigen.AuthzEntityKind) apigen.AuthzEntityTypeSelector {
	return apigen.AuthzEntityTypeSelector{AllEntityTypesExcluding: apigen.Some(apigen.AuthzEntityKindList{Values: ks})}
}

func exactTypes(ks ...apigen.AuthzEntityKind) apigen.AuthzEntityTypeSelector {
	return apigen.AuthzEntityTypeSelector{ExactEntityTypes: apigen.Some(apigen.AuthzEntityKindList{Values: ks})}
}

func refsExcluding(rs ...apigen.AuthzEntityRef) apigen.AuthzEntityRefSelector {
	return apigen.AuthzEntityRefSelector{AllEntityRefsExcluding: apigen.Some(apigen.AuthzEntityRefList{Values: rs})}
}

func exactRefs(rs ...apigen.AuthzEntityRef) apigen.AuthzEntityRefSelector {
	return apigen.AuthzEntityRefSelector{ExactEntityRefs: apigen.Some(apigen.AuthzEntityRefList{Values: rs})}
}

func refOf(v apigen.AuthzEntityRefTargetValueOneof) apigen.AuthzEntityRef {
	return apigen.AuthzEntityRef{Target: apigen.AuthzEntityRefTarget{Value: v}}
}

func deploymentRef(id uint64) apigen.AuthzEntityRef {
	return refOf(apigen.AuthzEntityRefTargetValueOneof{Deployment: &id})
}

func secretRef(id uint64) apigen.AuthzEntityRef {
	return refOf(apigen.AuthzEntityRefTargetValueOneof{Secret: &id})
}

func userRef(id uint64) apigen.AuthzEntityRef {
	return refOf(apigen.AuthzEntityRefTargetValueOneof{User: &id})
}

func systemConfigRef(id uint64) apigen.AuthzEntityRef {
	return refOf(apigen.AuthzEntityRefTargetValueOneof{SystemConfig: &id})
}

func tplPermissions(s apigen.AuthzPermissionSelector) apigen.AuthzTemplatePermissionSelector {
	return apigen.AuthzTemplatePermissionSelector{Value: apigen.AuthzTemplatePermissionSelectorValueOneof{Selector: &s}}
}

func tplSpaces(s apigen.AuthzSpaceSelector) apigen.AuthzTemplateSpaceSelector {
	return apigen.AuthzTemplateSpaceSelector{Value: apigen.AuthzTemplateSpaceSelectorValueOneof{Selector: &s}}
}

func tplSpacesArg(id uint32) apigen.AuthzTemplateSpaceSelector {
	return apigen.AuthzTemplateSpaceSelector{Value: apigen.AuthzTemplateSpaceSelectorValueOneof{Argument: &apigen.AuthzArgument{ArgumentID: id}}}
}

func tplTypes(s apigen.AuthzEntityTypeSelector) apigen.AuthzTemplateEntityTypeSelector {
	return apigen.AuthzTemplateEntityTypeSelector{Value: apigen.AuthzTemplateEntityTypeSelectorValueOneof{Selector: &s}}
}

func tplRefs(s apigen.AuthzEntityRefSelector) apigen.AuthzTemplateEntityRefSelector {
	return apigen.AuthzTemplateEntityRefSelector{Value: apigen.AuthzTemplateEntityRefSelectorValueOneof{Selector: &s}}
}

func tplRefsArg(id uint32) apigen.AuthzTemplateEntityRefSelector {
	return apigen.AuthzTemplateEntityRefSelector{Value: apigen.AuthzTemplateEntityRefSelectorValueOneof{Argument: &apigen.AuthzArgument{ArgumentID: id}}}
}

func literalString(v string) apigen.StringSetting {
	return apigen.StringSetting{Value: apigen.StringSettingValue{Value: apigen.StringSettingValueValueOneof{Literal: &v}}}
}

func configString(id uint64, version uint32) apigen.StringSetting {
	return apigen.StringSetting{Value: apigen.StringSettingValue{Value: apigen.StringSettingValueValueOneof{ConfigRef: &apigen.ConfigRef{ConfigID: id, Version: version}}}}
}

func literalBool(v bool) apigen.BoolSetting {
	return apigen.BoolSetting{Value: apigen.BoolSettingValue{Value: apigen.BoolSettingValueValueOneof{Literal: &v}}}
}

func configBool(id uint64, version uint32) apigen.BoolSetting {
	return apigen.BoolSetting{Value: apigen.BoolSettingValue{Value: apigen.BoolSettingValueValueOneof{ConfigRef: &apigen.ConfigRef{ConfigID: id, Version: version}}}}
}

func oldFullDeployment() *apigenold.Deployment {
	literal := "v"
	return &apigenold.Deployment{
		ID: 10, Name: "web", SpaceID: 2,
		Scheduling: apigenold.Scheduling{Running: true, DedicatedNodes: &apigenold.DedicatedNodesScheduling{Nodes: []int32{3}}, Generation: 4},
		Spec: apigenold.DeploymentSpec{
			Networking: apigenold.NetworkingConfig{
				PortForwarding: []*apigenold.PortForward{{
					Protocol: apigenold.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 8080, ContainerPort: 80,
					IpFilter: &apigenold.IpFilter{Allow: []string{"10.0.0.0/8", "192.168.1.5"}, Deny: []string{"10.1.0.0/16"}},
				}},
				Ingress: []*apigenold.Ingress{
					{
						Kind: apigenold.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH, Hostname: "tls.example",
						TlsPassthroughConfig: &apigenold.TlsPassthroughConfig{ContainerPort: 8443},
						Listen: []*apigenold.IngressListen{{
							Node:    &apigenold.NodeSelector{Any: true},
							Address: &apigenold.AddressSelector{Family: apigenold.AddressFamily_ADDRESS_FAMILY_IPV4},
						}},
					},
					{
						Kind: apigenold.IngressKind_INGRESS_KIND_HTTPS, Hostname: "web.example",
						HttpsConfig: &apigenold.HttpsConfig{
							ContainerPort: 80, PathPrefix: "/api", StripPrefix: true,
							CertSource: &apigenold.CertSource{Acme: &apigenold.AcmeCertSource{}},
						},
						Listen: []*apigenold.IngressListen{
							{
								Node:    &apigenold.NodeSelector{NodeID: 3},
								Address: &apigenold.AddressSelector{Family: apigenold.AddressFamily_ADDRESS_FAMILY_IPV6, Prefixes: []string{"fd00::/8", "fd00::1"}},
							},
							{Address: &apigenold.AddressSelector{}},
							{Node: &apigenold.NodeSelector{}},
						},
					},
				},
			},
			Container1Spec: &apigenold.ContainerSpec{
				Source:          apigenold.ContainerBundleSource{NixDockerBuild: &apigenold.NixDockerBuild{Repo: "r", Flake: "f", Target: "t"}},
				Version:         "abc",
				ReadinessSignal: &apigenold.ContainerReadinessSignal{},
				Runtime: apigenold.ContainerRuntime{
					User: "app", OverrideCommand: []string{"/bin/x"}, OverrideWorkingDir: "/w",
					EnvVars: map[string]*apigenold.EnvVarValue{
						"LIT":  {Value: &literal},
						"SEC":  {Secret: vref(5, 2)},
						"CFG":  {Config: vref(6, 1)},
						"AST":  {Asset: "logo.png", AssetRef: vref(8, 3)},
						"ADDR": {AddressDeploymentID: i32p(7), AddressSpaceID: i32p(2)},
					},
					DefaultVolume:         apigenold.DefaultVolumeMount{ContainerPath: "/data"},
					CrossDeploymentMounts: []*apigenold.CrossDeploymentMount{{DeploymentID: 11, ContainerPath: "/x", Permission: apigenold.FilePermission_READ_ONLY}},
					AssetMounts:           []*apigenold.AssetMount{{ContainerPath: "/a", Permission: apigenold.FilePermission_READ_EXECUTE, Asset: apigenold.ValueRef{ID: 8, Version: 3}}},
					Mounts:                []*apigenold.CustomHostMount{{HostPath: "/h", ContainerPath: "/c", Permission: apigenold.FilePermission_READ_WRITE}},
					IssuedTlsMount:        &apigenold.IssuedTLSMount{ContainerPath: "/tls", ExtraNames: []string{"a.b"}},
				},
			},
		},
	}
}

func newFullDeployment() apigen.Deployment {
	return apigen.Deployment{
		ID: 10, Name: "web", SpaceID: 2,
		Scheduling: apigen.Scheduling{Running: true, Placement: apigen.Placement{Value: apigen.PlacementValueOneof{DedicatedNodes: &apigen.DedicatedNodesScheduling{Nodes: []uint64{3}}}}, RestartGeneration: 4},
		Spec: apigen.DeploymentSpec{
			Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
				Source: apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{NixImageBuild: &apigen.NixImageBuild{Repo: "r", Flake: "f", Target: "t"}}},
				Runtime: apigen.ContainerRuntime{
					User: "app",
					EnvVars: map[string]apigen.EnvVar{
						"LIT":  {Value: apigen.EnvVarValueOneof{Literal: &apigen.LiteralEnv{Value: "v"}}},
						"SEC":  {Value: apigen.EnvVarValueOneof{Secret: &apigen.SecretEnv{Secret: apigen.SecretRef{SecretID: 5, Version: 2}}}},
						"CFG":  {Value: apigen.EnvVarValueOneof{Config: &apigen.ConfigEnv{Config: apigen.ConfigRef{ConfigID: 6, Version: 1}}}},
						"AST":  {Value: apigen.EnvVarValueOneof{Asset: &apigen.AssetEnv{Key: "logo.png", Asset: apigen.AssetRef{AssetID: 8, Version: 3}}}},
						"ADDR": {Value: apigen.EnvVarValueOneof{Address: &apigen.AddressEnv{DeploymentID: 7, SpaceID: 2}}},
					},
					OverrideCommand: []string{"/bin/x"}, OverrideWorkingDir: "/w",
					DefaultVolume:         apigen.DefaultVolumeMount{ContainerPath: "/data"},
					CrossDeploymentMounts: []apigen.CrossDeploymentMount{{DeploymentID: 11, ContainerPath: "/x", Permission: apigen.FilePermission_FILE_PERMISSION_READ_ONLY}},
					Mounts:                []apigen.HostMount{{HostPath: "/h", ContainerPath: "/c", Permission: apigen.FilePermission_FILE_PERMISSION_READ_WRITE}},
					AssetMounts:           []apigen.AssetMount{{Asset: apigen.AssetRef{AssetID: 8, Version: 3}, ContainerPath: "/a", Permission: apigen.FilePermission_FILE_PERMISSION_READ_EXECUTE}},
					IssuedTlsMount:        apigen.Some(apigen.IssuedTLSMount{ContainerPath: "/tls", ExtraNames: []string{"a.b"}}),
				},
				Version:         "abc",
				UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
				ReadinessSignal: apigen.Some(apigen.ContainerReadinessSignal{}),
			}}},
			Networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST,
				PortForwarding: []apigen.PortForward{{
					Protocol: apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 8080, ContainerPort: 80,
					IpFilter: []apigen.IpFilter{
						{Mode: apigen.IpFilterMode_IP_FILTER_MODE_ALLOW, Prefix: pfx("10.0.0.0/8")},
						{Mode: apigen.IpFilterMode_IP_FILTER_MODE_ALLOW, Prefix: pfx("192.168.1.5/32")},
						{Mode: apigen.IpFilterMode_IP_FILTER_MODE_DENY, Prefix: pfx("10.1.0.0/16")},
					},
				}},
				Ingress: []apigen.Ingress{
					{
						Hostname: "tls.example",
						Listen: []apigen.IngressListen{{
							Node:      apigen.Some(apigen.IngressNode{Value: apigen.IngressNodeValueOneof{Any: &apigen.AnyNode{}}}),
							Addresses: []apigen.IpPrefix{pfx("0.0.0.0/0")},
						}},
						Config: apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{TlsPassthrough: &apigen.TlsPassthroughConfig{ContainerPort: 8443}}},
					},
					{
						Hostname: "web.example",
						Listen: []apigen.IngressListen{
							{
								Node:      apigen.Some(apigen.IngressNode{Value: apigen.IngressNodeValueOneof{Specific: &apigen.SpecificNode{NodeID: 3}}}),
								Addresses: []apigen.IpPrefix{pfx("fd00::/8"), pfx("fd00::1/128")},
							},
							{},
							{},
						},
						Config: apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{Https: &apigen.HttpsConfig{
							ContainerPort: 80, PathPrefix: "/api", StripPrefix: true,
							BackendProtocol: apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_HTTP1,
							CertSource:      apigen.Some(apigen.CertSource{Value: apigen.CertSourceValueOneof{Acme: &apigen.AcmeCertSource{Challenge: apigen.AcmeChallenge_ACME_CHALLENGE_HTTP_01}}}),
						}}},
					},
				},
			},
		},
	}
}

func oldRemoteDeployment() *apigenold.Deployment {
	return &apigenold.Deployment{
		ID: 12, Name: "api", SpaceID: 1,
		Scheduling: apigenold.Scheduling{DedicatedNodes: &apigenold.DedicatedNodesScheduling{Nodes: []int32{1}}},
		Spec: apigenold.DeploymentSpec{
			Networking: apigenold.NetworkingConfig{
				Mode: apigenold.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []*apigenold.Ingress{
					{
						Kind: apigenold.IngressKind_INGRESS_KIND_HTTPS, Hostname: "api.example",
						HttpsConfig: &apigenold.HttpsConfig{
							ContainerPort: 8080, BackendProtocol: apigenold.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_H2C,
							MaxRequestBodyBytes: 1024, FlushIntervalMs: 50,
							CertSource: &apigenold.CertSource{Secret: &apigenold.SecretCertSource{Secret: apigenold.ValueRef{ID: 9, Version: 1}}},
						},
					},
					{
						Kind: apigenold.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH, Hostname: "pg.example",
						TlsPassthroughConfig: &apigenold.TlsPassthroughConfig{HostPort: 5432, ContainerPort: 5432},
					},
				},
			},
			Container1Spec: &apigenold.ContainerSpec{
				Source:          apigenold.ContainerBundleSource{RemoteImage: &apigenold.RemoteDockerImage{Image: "ghcr.io/x:1"}},
				Version:         "1",
				UpgradeStrategy: apigenold.ContainerUpgradeStrategy_ROLLOVER,
				ReadinessSignal: &apigenold.ContainerReadinessSignal{TimeoutSeconds: 30},
				Runtime: apigenold.ContainerRuntime{
					DefaultVolume: apigenold.DefaultVolumeMount{Disabled: true}, DevShmSizeKb: 64, FileDescriptorLimit: 1024,
				},
			},
		},
	}
}

func newRemoteDeployment() apigen.Deployment {
	return apigen.Deployment{
		ID: 12, Name: "api", SpaceID: 1,
		Scheduling: apigen.DedicatedScheduling(false, 1),
		Spec: apigen.DeploymentSpec{
			Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
				Source: apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "ghcr.io/x:1"}}},
				Runtime: apigen.ContainerRuntime{
					DefaultVolume: apigen.DefaultVolumeMount{Disabled: true}, DevShmSizeKb: apigen.Some[uint32](64), FileDescriptorLimit: apigen.Some[uint32](1024),
				},
				Version:         "1",
				UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_ROLLOVER,
				ReadinessSignal: apigen.Some(apigen.ContainerReadinessSignal{TimeoutSeconds: apigen.Some[uint32](30)}),
			}}},
			Networking: apigen.NetworkingConfig{
				Mode: apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL,
				Ingress: []apigen.Ingress{
					{
						Hostname: "api.example",
						Config: apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{Https: &apigen.HttpsConfig{
							ContainerPort: 8080, BackendProtocol: apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_H2C,
							MaxRequestBodyBytes: apigen.Some[uint64](1024), FlushIntervalMs: apigen.Some[uint32](50),
							CertSource: apigen.Some(apigen.CertSource{Value: apigen.CertSourceValueOneof{Secret: &apigen.SecretCertSource{Secret: apigen.SecretRef{SecretID: 9, Version: 1}}}}),
						}}},
					},
					{
						Hostname: "pg.example",
						Config:   apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{TlsPassthrough: &apigen.TlsPassthroughConfig{HostPort: apigen.Some[uint32](5432), ContainerPort: 5432}}},
					},
				},
			},
		},
	}
}

func oldSelfDeployment() *apigenold.Deployment {
	return &apigenold.Deployment{
		ID: 1, Name: internaldeploy.SelfName, SpaceID: 0,
		Scheduling: apigenold.Scheduling{Running: true, DedicatedNodes: &apigenold.DedicatedNodesScheduling{Nodes: []int32{1}}},
		Spec:       apigenold.DeploymentSpec{OpendeploySpec: &apigenold.OpendeploySpec{Version: "v0.0.615"}},
	}
}

func newSelfDeployment() apigen.Deployment {
	spec := internaldeploy.SelfSpec()
	spec.Workload.Value.Container.Version = "v0.0.615"
	return apigen.Deployment{ID: 1, Name: internaldeploy.SelfName, Scheduling: apigen.DedicatedScheduling(true, 1), Spec: *spec}
}

func oldTemplate() *apigenold.AuthzRuleTemplate {
	return &apigenold.AuthzRuleTemplate{
		ID: 2, Name: "space_admin", Builtin: true,
		Spec: &apigenold.AuthzRuleTemplateSpec{
			Arguments: []*apigenold.AuthzTemplateArgument{{ID: 1, Name: "spaces"}, {ID: 2, Name: "deployments"}},
			Rules: []*apigenold.AuthzRule{
				{Permissions: sel(true, 0, nil, []int64{7, 8}), Spaces: sel(false, 1, nil, nil), EntityTypes: sel(true, 0, nil, nil), EntityRefs: sel(true, 0, nil, nil)},
				{Permissions: sel(false, 0, []int64{4, 1}, nil), Spaces: sel(false, 1, nil, nil), EntityTypes: sel(false, 0, []int64{3}, nil), EntityRefs: sel(true, 0, nil, []int64{5}), DelegationAllowed: true},
				{Permissions: sel(false, 0, []int64{4}, nil), Spaces: sel(false, 0, []int64{1, 2}, []int64{2}), EntityTypes: sel(false, 0, []int64{2}, nil), EntityRefs: sel(false, 0, []int64{10, 11, 10}, []int64{11})},
				{Permissions: sel(false, 0, []int64{2}, nil), Spaces: sel(true, 0, nil, nil), EntityTypes: sel(false, 0, []int64{2}, nil), EntityRefs: sel(false, 2, nil, nil)},
			},
		},
	}
}

func newTemplate() apigen.AuthzGrantTemplate {
	return apigen.AuthzGrantTemplate{
		ID: 2, Name: "space_admin", Builtin: true,
		Spec: apigen.AuthzGrantTemplateSpec{
			Arguments: []apigen.AuthzTemplateArgument{
				{ID: 1, Name: "spaces", Kind: apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_SPACE},
				{ID: 2, Name: "deployments", Kind: apigen.AuthzArgumentKind_AUTHZ_ARGUMENT_KIND_ENTITY_REF},
			},
			Rules: []apigen.AuthzTemplateRule{
				{Effect: allow(false), Selector: apigen.AuthzTemplateSelector{
					Permissions: tplPermissions(verbsExcluding(apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_MOUNTS, apigen.AuthzVerb_AUTHZ_VERB_USE_HOST_NETWORK)),
					Spaces:      tplSpacesArg(1),
					EntityTypes: tplTypes(typesExcluding()),
					EntityRefs:  tplRefs(refsExcluding()),
				}},
				{Effect: allow(true), Selector: apigen.AuthzTemplateSelector{
					Permissions: tplPermissions(exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW, apigen.AuthzVerb_AUTHZ_VERB_CREATE)),
					Spaces:      tplSpacesArg(1),
					EntityTypes: tplTypes(exactTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_SECRET)),
					EntityRefs:  tplRefs(refsExcluding(secretRef(5))),
				}},
				{Effect: allow(false), Selector: apigen.AuthzTemplateSelector{
					Permissions: tplPermissions(exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW)),
					Spaces:      tplSpaces(exactSpaces(1)),
					EntityTypes: tplTypes(exactTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT)),
					EntityRefs:  tplRefs(exactRefs(deploymentRef(10))),
				}},
				{Effect: allow(false), Selector: apigen.AuthzTemplateSelector{
					Permissions: tplPermissions(exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_UPDATE)),
					Spaces:      tplSpaces(spacesExcluding()),
					EntityTypes: tplTypes(exactTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_DEPLOYMENT)),
					EntityRefs:  tplRefsArg(2),
				}},
			},
		},
	}
}

func oldInternalUser() []byte {
	return (&apigenold.InternalUser{
		ID: 4, WebAuthNID: []byte("wid"), Name: "alice", Delegated: true,
		Credentials: []*apigenold.WebAuthnCredential{{ID: []byte("c1"), Data: []byte(`{"a":1}`)}, {ID: []byte("c2"), Data: []byte("{}")}},
	}).Encode()
}

func newUserAuthentication() apigen.UserAuthentication {
	return apigen.UserAuthentication{
		WebAuthnID:  []byte("wid"),
		Credentials: []apigen.WebAuthnCredential{{ID: []byte("c1"), Data: []byte(`{"a":1}`)}, {ID: []byte("c2"), Data: []byte("{}")}},
	}
}

func oldSystemConfig() *apigenold.SystemConfig {
	return &apigenold.SystemConfig{
		NetworkUlaPrefix: []byte{0xfd, 1, 2, 3, 4, 5},
		Settings: apigenold.ClusterSettings{
			HttpWeb: apigenold.HttpWebSettings{Enabled: apigenold.BoolSetting{Value: true}, Listen: apigenold.StringSetting{Value: ":80"}},
			HttpsWeb: apigenold.HttpsWebSettings{
				Enabled:    apigenold.BoolSetting{ConfigRef: apigenold.ConfigRef{Ref: apigenold.ValueRef{ID: 6, Version: 2}}},
				Listen:     apigenold.StringSetting{Value: ":443", ConfigRef: apigenold.ConfigRef{Ref: apigenold.ValueRef{ID: 7, Version: 1}}},
				TlsCertPem: apigenold.SecretRef{Ref: apigenold.ValueRef{ID: 5, Version: 1}},
				AcmeHosts:  apigenold.StringSetting{Value: "a.example,b.example"},
			},
			Cluster: apigenold.ClusterListenSettings{Listen: apigenold.StringSetting{Value: ":7443"}},
			Backup:  apigenold.BackupSettings{Enabled: apigenold.BoolSetting{Value: true}, S3SecretAccessKey: apigenold.SecretRef{Ref: apigenold.ValueRef{ID: 3, Version: 4}}},
			LargeAssets: apigenold.LargeAssetsSettings{
				KeepLocalCopy: apigenold.BoolSetting{Value: true, ConfigRef: apigenold.ConfigRef{Ref: apigenold.ValueRef{ID: 8, Version: 1}}},
			},
			Auth: apigenold.AuthSettings{PasswordLoginEnabled: apigenold.BoolSetting{Value: true}},
		},
	}
}

func newSystemConfig() apigen.SystemConfig {
	return apigen.SystemConfig{
		ID:               1,
		NetworkUlaPrefix: []byte{0xfd, 1, 2, 3, 4, 5},
		Settings: apigen.ClusterSettings{
			HttpWeb: apigen.HttpWebSettings{Enabled: literalBool(true), Listen: literalString(":80")},
			HttpsWeb: apigen.HttpsWebSettings{
				Enabled: configBool(6, 2), Listen: configString(7, 1), TlsSelfManaged: literalBool(false),
				TlsCertPem: apigen.Some(apigen.SecretRef{SecretID: 5, Version: 1}),
				AcmeHosts:  literalString("a.example,b.example"), AcmeEmail: literalString(""),
			},
			Cluster: apigen.ClusterListenSettings{Listen: literalString(":7443"), EnrollmentListen: literalString("")},
			Repo:    apigen.RepoSettings{},
			Backup: apigen.BackupSettings{
				Enabled: literalBool(true), S3AccessKeyID: literalString(""), S3SecretAccessKey: apigen.Some(apigen.SecretRef{SecretID: 3, Version: 4}),
				S3Bucket: literalString(""), S3Path: literalString(""), S3Region: literalString(""), S3Endpoint: literalString(""),
			},
			LargeAssets: apigen.LargeAssetsSettings{
				UseSeparateS3: literalBool(false), S3AccessKeyID: literalString(""), S3Bucket: literalString(""), S3Path: literalString(""),
				S3Region: literalString(""), S3Endpoint: literalString(""), KeepLocalCopy: configBool(8, 1),
			},
			Auth: apigen.AuthSettings{PasswordLoginEnabled: literalBool(true)},
		},
	}
}

func oldFullStatus() *apigenold.ScheduledInstanceStatus {
	return &apigenold.ScheduledInstanceStatus{
		UpdatedAt: ms(1000), ScheduledInstanceID: 5, DeploymentID: 10,
		Preparer: apigenold.PreparerStatus{DeploymentSpecVersion: 2, Artifact: "sha", Inputs: apigenold.InputsStatus_INPUTS_READY, Image: apigenold.ImageStatus_IMAGE_READY},
		Runner: apigenold.RunnerStatus{
			DeploymentSpecVersion: 2, RunningPid: 123, RunningArtifact: "sha", Status: apigenold.RunningStatus_RUNNING, NumberOfRestarts: 1,
			LastRestartAt: ms(2000), RunningVersion: "abc", NetworkDiagnostics: []string{"d"}, ExitCode: i32p(3),
		},
	}
}

func newFullStatus() apigen.ScheduledInstanceStatus {
	return apigen.ScheduledInstanceStatus{
		ScheduledInstanceID: 5, UpdatedAt: apigen.Some(ms(1000)),
		Preparer: apigen.Some(apigen.PreparerStatus{DeploymentSpecVersion: 2, Artifact: "sha", Inputs: apigen.InputsStatus_INPUTS_STATUS_READY, Image: apigen.Some(apigen.ImageStatus_IMAGE_STATUS_READY)}),
		Runner: apigen.Some(apigen.RunnerStatus{
			DeploymentSpecVersion: 2, RunningPid: apigen.Some[uint32](123), RunningArtifact: "sha", Status: apigen.RunningStatus_RUNNING_STATUS_RUNNING, NumberOfRestarts: 1,
			LastRestartAt: apigen.Some(ms(2000)), NetworkDiagnostics: []string{"d"}, ExitCode: apigen.Some[int32](3),
		}),
	}
}

func templates(t apigen.AuthzGrantTemplate) TemplateLookup {
	return func(id uint64) (*apigen.AuthzGrantTemplate, bool) {
		if id != t.ID {
			return nil, false
		}
		return &t, true
	}
}

func entityOf(v apigen.CoreEntityValueOneof) apigen.CoreEntity { return apigen.CoreEntity{Value: v} }

func TestEntity(t *testing.T) {
	sha := strings.Repeat("ab", 32)
	keyslotIDs := func(old apigenold.SecretKeyslot) uint64 {
		if old.Kind == apigenold.SecretKeyslotKind_SECRET_KEYSLOT_RECOVERY {
			return 100
		}
		return 100 + uint64(old.NodeID)
	}
	full := newFullDeployment()
	remote := newRemoteDeployment()
	self := newSelfDeployment()
	tpl := newTemplate()
	sysCfg := newSystemConfig()
	sysCfgHashed := newSystemConfig()
	sysCfgHashed.MasterPasswordHash = apigen.Some("hash")
	fullStatus := newFullStatus()
	cases := []struct {
		name string
		t    apigen.CoreEntityType
		old  apigenold.CoreEntity
		ids  IDs
		want apigen.CoreEntity
	}{
		{
			name: "deployment with every env var arm, nix build, zero numerics absent, defaults applied",
			t:    apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT,
			old:  apigenold.CoreEntity{Deployment: oldFullDeployment()},
			want: entityOf(apigen.CoreEntityValueOneof{Deployment: &full}),
		},
		{
			name: "deployment with remote image, rollover, present numerics, h2c, secret cert",
			t:    apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT,
			old:  apigenold.CoreEntity{Deployment: oldRemoteDeployment()},
			want: entityOf(apigen.CoreEntityValueOneof{Deployment: &remote}),
		},
		{
			name: "deployment in host mode",
			t:    apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT,
			old: apigenold.CoreEntity{Deployment: &apigenold.Deployment{
				ID: 13, Name: "host", SpaceID: 1, Scheduling: apigenold.Scheduling{DedicatedNodes: &apigenold.DedicatedNodesScheduling{Nodes: []int32{1}}},
				Spec: apigenold.DeploymentSpec{
					Networking:     apigenold.NetworkingConfig{Mode: apigenold.NetworkingMode_NETWORKING_MODE_HOST},
					Container1Spec: &apigenold.ContainerSpec{Source: apigenold.ContainerBundleSource{RemoteImage: &apigenold.RemoteDockerImage{Image: "img"}}, UpgradeStrategy: apigenold.ContainerUpgradeStrategy_RECREATE},
				},
			}},
			want: entityOf(apigen.CoreEntityValueOneof{Deployment: &apigen.Deployment{
				ID: 13, Name: "host", SpaceID: 1, Scheduling: apigen.DedicatedScheduling(false, 1),
				Spec: apigen.DeploymentSpec{
					Workload: apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
						Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: "img"}}},
						UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
					}}},
					Networking: apigen.NetworkingConfig{Mode: apigen.NetworkingMode_NETWORKING_MODE_HOST},
				},
			}}),
		},
		{
			name: "opendeploy spec becomes the self container spec",
			t:    apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT,
			old:  apigenold.CoreEntity{Deployment: oldSelfDeployment()},
			want: entityOf(apigen.CoreEntityValueOneof{Deployment: &self}),
		},
		{
			name: "scheduled instance",
			t:    apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE,
			old: apigenold.CoreEntity{ScheduledInstance: &apigenold.ScheduledInstance{
				ID: 5, DeploymentID: 10, NodeID: 3, InstanceOrdinal: 1, State: apigenold.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY,
				DeploymentVersion: 7, DeploymentSpecVersion: 2, SpaceID: 2,
			}},
			want: entityOf(apigen.CoreEntityValueOneof{ScheduledInstance: &apigen.ScheduledInstance{
				ID: 5, NodeID: 3, InstanceOrdinal: 1, State: apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_STANDBY, SpaceID: 2,
				Deployment: apigen.DeploymentRef{DeploymentID: 10, Version: 7},
			}}),
		},
		{
			name: "scheduled instance status with both halves",
			t:    apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS,
			old:  apigenold.CoreEntity{ScheduledInstanceStatus: oldFullStatus()},
			want: entityOf(apigen.CoreEntityValueOneof{ScheduledInstanceStatus: &fullStatus}),
		},
		{
			name: "scheduled instance status with preparer only and zero fields absent",
			t:    apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS,
			old: apigenold.CoreEntity{ScheduledInstanceStatus: &apigenold.ScheduledInstanceStatus{
				UpdatedAt: ms(1000), ScheduledInstanceID: 5, Preparer: apigenold.PreparerStatus{DeploymentSpecVersion: 1, Inputs: apigenold.InputsStatus_INPUTS_RESOLVING},
			}},
			want: entityOf(apigen.CoreEntityValueOneof{ScheduledInstanceStatus: &apigen.ScheduledInstanceStatus{
				ScheduledInstanceID: 5, UpdatedAt: apigen.Some(ms(1000)),
				Preparer: apigen.Some(apigen.PreparerStatus{DeploymentSpecVersion: 1, Inputs: apigen.InputsStatus_INPUTS_STATUS_RESOLVING}),
			}}),
		},
		{
			name: "scheduled instance status with runner only and zero fields absent",
			t:    apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS,
			old: apigenold.CoreEntity{ScheduledInstanceStatus: &apigenold.ScheduledInstanceStatus{
				UpdatedAt: ms(1000), ScheduledInstanceID: 5, Runner: apigenold.RunnerStatus{DeploymentSpecVersion: 1, Status: apigenold.RunningStatus_STOPPED},
			}},
			want: entityOf(apigen.CoreEntityValueOneof{ScheduledInstanceStatus: &apigen.ScheduledInstanceStatus{
				ScheduledInstanceID: 5, UpdatedAt: apigen.Some(ms(1000)),
				Runner: apigen.Some(apigen.RunnerStatus{DeploymentSpecVersion: 1, Status: apigen.RunningStatus_RUNNING_STATUS_STOPPED}),
			}}),
		},
		{
			name: "node with every field",
			t:    apigen.CoreEntityType_CORE_ENTITY_NODE,
			old: apigenold.CoreEntity{Node: &apigenold.Node{
				ID: 3, Status: apigenold.NodeLifecycleStatus_NODE_MEMBER_NORMAL, EnrollmentRequestedAt: 5000,
				Operator: apigenold.NodeOperator{Name: "n1", Roles: []int32{1}, AllowedSpaces: []int32{1, 2}, EnrolledTime: 6000},
				Reported: apigenold.NodeReported{Identifier: "m-1", UnderlayAddress: "10.0.0.2", WgPublicKey: "pk", HostAddresses: []string{"10.0.0.2", "fd00::2"}},
			}},
			want: entityOf(apigen.CoreEntityValueOneof{Node: &apigen.Node{
				ID: 3, Status: apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_MEMBER_NORMAL, EnrollmentRequestedAt: apigen.Some(ms(5000)),
				Operator: apigen.NodeOperator{Name: "n1", Roles: []apigen.NodeRole{apigen.NodeRole_NODE_ROLE_SECONDARY}, AllowedSpaces: []uint64{1, 2}, EnrolledTime: apigen.Some(ms(6000))},
				Reported: apigen.NodeReported{Identifier: "m-1", UnderlayAddress: addr("10.0.0.2"), WgPublicKey: "pk", HostAddresses: []apigen.IpAddress{addr("10.0.0.2"), addr("fd00::2")}},
			}}),
		},
		{
			name: "node with zero times absent and the primary role",
			t:    apigen.CoreEntityType_CORE_ENTITY_NODE,
			old: apigenold.CoreEntity{Node: &apigenold.Node{
				ID: 1, Status: apigenold.NodeLifecycleStatus_NODE_ENROLLMENT_REQUESTED,
				Operator: apigenold.NodeOperator{Roles: []int32{0}},
				Reported: apigenold.NodeReported{Identifier: "m", UnderlayAddress: "10.0.0.9", HostAddressesUnknown: true},
			}},
			want: entityOf(apigen.CoreEntityValueOneof{Node: &apigen.Node{
				ID: 1, Status: apigen.NodeLifecycleStatus_NODE_LIFECYCLE_STATUS_ENROLLMENT_REQUESTED,
				Operator: apigen.NodeOperator{Roles: []apigen.NodeRole{apigen.NodeRole_NODE_ROLE_PRIMARY}},
				Reported: apigen.NodeReported{Identifier: "m", UnderlayAddress: addr("10.0.0.9"), HostAddressesUnknown: true},
			}}),
		},
		{
			name: "node status connected",
			t:    apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS,
			old: apigenold.CoreEntity{NodeStatus: &apigenold.NodeStatus{
				NodeID: 3, UpdatedAt: ms(1000), IsConnected: true, LastConnectedAt: ms(900), RemoteAddress: "1.2.3.4:5", OpendeployVersion: "v1", RuntimeVersions: "r",
			}},
			want: entityOf(apigen.CoreEntityValueOneof{NodeStatus: &apigen.NodeStatus{
				NodeID: 3, UpdatedAt: apigen.Some(ms(1000)), IsConnected: true, LastConnectedAt: apigen.Some(ms(900)), RemoteAddress: "1.2.3.4:5", OpendeployVersion: "v1", RuntimeVersions: "r",
			}}),
		},
		{
			name: "node status never connected",
			t:    apigen.CoreEntityType_CORE_ENTITY_NODE_STATUS,
			old:  apigenold.CoreEntity{NodeStatus: &apigenold.NodeStatus{NodeID: 3, UpdatedAt: ms(1000)}},
			want: entityOf(apigen.CoreEntityValueOneof{NodeStatus: &apigen.NodeStatus{NodeID: 3, UpdatedAt: apigen.Some(ms(1000))}}),
		},
		{
			name: "sealed secret in a directory",
			t:    apigen.CoreEntityType_CORE_ENTITY_SECRET,
			old: apigenold.CoreEntity{Secret: &apigenold.Secret{
				ID: 5, Fs: &apigenold.SecretFs{Name: "db/pass", DirectoryID: 2}, SpaceID: 1, SmkVersion: 1, Ciphertext: []byte{1, 2}, Nonce: []byte{3},
			}},
			want: entityOf(apigen.CoreEntityValueOneof{Secret: &apigen.Secret{
				ID: 5, Fs: apigen.SecretFs{Key: "db/pass", DirectoryID: apigen.Some[uint64](2)}, SpaceID: 1,
				Sealed: apigen.Some(apigen.SealedSecret{SmkVersion: 1, Ciphertext: []byte{1, 2}, Nonce: []byte{3}}),
			}}),
		},
		{
			name: "secret without ciphertext at the root",
			t:    apigen.CoreEntityType_CORE_ENTITY_SECRET,
			old:  apigenold.CoreEntity{Secret: &apigenold.Secret{ID: 5, Fs: &apigenold.SecretFs{Name: "pass"}, SpaceID: 1}},
			want: entityOf(apigen.CoreEntityValueOneof{Secret: &apigen.Secret{ID: 5, Fs: apigen.SecretFs{Key: "pass"}, SpaceID: 1}}),
		},
		{
			name: "config",
			t:    apigen.CoreEntityType_CORE_ENTITY_CONFIG,
			old:  apigenold.CoreEntity{Config: &apigenold.Config{ID: 6, Fs: &apigenold.ConfigFs{Name: "cfg", DirectoryID: 3}, SpaceID: 1, Value: "x"}},
			want: entityOf(apigen.CoreEntityValueOneof{Config: &apigen.Config{ID: 6, Fs: apigen.ConfigFs{Key: "cfg", DirectoryID: apigen.Some[uint64](3)}, SpaceID: 1, Value: "x"}}),
		},
		{
			name: "asset with a hex digest",
			t:    apigen.CoreEntityType_CORE_ENTITY_ASSET,
			old:  apigenold.CoreEntity{Asset: &apigenold.Asset{ID: 8, Fs: &apigenold.AssetFs{Key: "logo.png", DirectoryID: 4}, SpaceID: 1, Sha256: sha, SizeBytes: 123, StorageKey: "sk"}},
			want: entityOf(apigen.CoreEntityValueOneof{Asset: &apigen.Asset{
				ID: 8, Fs: apigen.AssetFs{Key: "logo.png", DirectoryID: apigen.Some[uint64](4)}, SpaceID: 1, SizeBytes: 123, Sha256: bytes.Repeat([]byte{0xab}, 32), StorageKey: "sk",
			}}),
		},
		{
			name: "network policy with space and deployment peers and an open-ended port",
			t:    apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY,
			old: apigenold.CoreEntity{NetworkPolicy: &apigenold.NetworkPolicy{
				ID: 2, Action: apigenold.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
				Source:      &apigenold.NetworkPolicyPeerRef{Kind: apigenold.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1},
				Destination: &apigenold.NetworkPolicyPeerRef{Kind: apigenold.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_DEPLOYMENT, ID: 10},
				Ports: []*apigenold.NetPortMatch{
					{Protocol: apigenold.NetProtocol_NET_PROTOCOL_TCP, Port: 80},
					{Protocol: apigenold.NetProtocol_NET_PROTOCOL_UDP, Port: 1000, PortEnd: 2000},
				},
			}},
			want: entityOf(apigen.CoreEntityValueOneof{NetworkPolicy: &apigen.NetworkPolicy{
				ID: 2, Action: apigen.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW,
				Source:      apigen.NetworkPolicyPeer{Target: apigen.NetworkPolicyPeerTarget{Value: apigen.NetworkPolicyPeerTargetValueOneof{Space: &apigen.SpacePeer{SpaceID: 1}}}},
				Destination: apigen.NetworkPolicyPeer{Target: apigen.NetworkPolicyPeerTarget{Value: apigen.NetworkPolicyPeerTargetValueOneof{Deployment: &apigen.DeploymentPeer{DeploymentID: 10}}}},
				Ports: []apigen.NetPortMatch{
					{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Range: apigen.PortRange{Start: 80, End: 80}},
					{Protocol: apigen.NetProtocol_NET_PROTOCOL_UDP, Range: apigen.PortRange{Start: 1000, End: 2000}},
				},
			}}),
		},
		{
			name: "space",
			t:    apigen.CoreEntityType_CORE_ENTITY_SPACE,
			old:  apigenold.CoreEntity{Space: &apigenold.Space{ID: 1, Name: "prod"}},
			want: entityOf(apigen.CoreEntityValueOneof{Space: &apigen.Space{ID: 1, Name: "prod"}}),
		},
		{
			name: "user with the nested internal user lifted",
			t:    apigen.CoreEntityType_CORE_ENTITY_USER,
			old:  apigenold.CoreEntity{User: &apigenold.User{ID: 4, Name: "alice", Credentials: oldInternalUser()}},
			want: entityOf(apigen.CoreEntityValueOneof{User: &apigen.User{ID: 4, Name: "alice", Authentication: newUserAuthentication()}}),
		},
		{
			name: "user whose name only the nested blob carries",
			t:    apigen.CoreEntityType_CORE_ENTITY_USER,
			old:  apigenold.CoreEntity{User: &apigenold.User{ID: 4, Credentials: oldInternalUser()}},
			want: entityOf(apigen.CoreEntityValueOneof{User: &apigen.User{ID: 4, Name: "alice", Authentication: newUserAuthentication()}}),
		},
		{
			name: "value directory at the root",
			t:    apigen.CoreEntityType_CORE_ENTITY_VALUE_DIRECTORY,
			old:  apigenold.CoreEntity{ValueDirectory: &apigenold.ValueDirectory{ID: 2, SpaceID: 1, Name: "db"}},
			want: entityOf(apigen.CoreEntityValueOneof{ValueDirectory: &apigen.ValueDirectory{ID: 2, SpaceID: 1, Key: "db"}}),
		},
		{
			name: "asset directory with a parent",
			t:    apigen.CoreEntityType_CORE_ENTITY_ASSET_DIRECTORY,
			old:  apigenold.CoreEntity{AssetDirectory: &apigenold.AssetDirectory{ID: 4, SpaceID: 1, Key: "img", ParentID: 3}},
			want: entityOf(apigen.CoreEntityValueOneof{AssetDirectory: &apigen.AssetDirectory{ID: 4, SpaceID: 1, Key: "img", ParentID: apigen.Some[uint64](3)}}),
		},
		{
			name: "grant template with every selector combination",
			t:    apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE,
			old:  apigenold.CoreEntity{AuthzRuleTemplate: oldTemplate()},
			want: entityOf(apigen.CoreEntityValueOneof{AuthzGrantTemplate: &tpl}),
		},
		{
			name: "rule grant",
			t:    apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT,
			ids:  IDs{Template: templates(tpl)},
			old: apigenold.CoreEntity{AuthzGrant: &apigenold.AuthzGrant{ID: 7, UserID: 4, Spec: &apigenold.AuthzGrantSpec{Rule: &apigenold.AuthzRule{
				Permissions: sel(true, 0, nil, nil), Spaces: sel(false, 0, []int64{1}, nil), EntityTypes: sel(false, 0, []int64{8}, nil), EntityRefs: sel(false, 0, []int64{4}, nil), DelegationAllowed: true,
			}}}},
			want: entityOf(apigen.CoreEntityValueOneof{AuthzGrant: &apigen.AuthzGrant{ID: 7, UserID: 4, Grant: apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{Rule: &apigen.AuthzRule{
				Effect:   allow(true),
				Selector: apigen.AuthzSelector{Permissions: verbsExcluding(), Spaces: exactSpaces(1), EntityTypes: exactTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER), EntityRefs: exactRefs(userRef(4))},
			}}}}}),
		},
		{
			name: "template grant with space and entity ref bindings",
			t:    apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT,
			ids:  IDs{Template: templates(tpl)},
			old: apigenold.CoreEntity{AuthzGrant: &apigenold.AuthzGrant{ID: 8, UserID: 4, TemplateID: 2, Spec: &apigenold.AuthzGrantSpec{Args: []*apigenold.AuthzArgumentBinding{
				{ArgumentID: 1, Values: []int64{1, 2}}, {ArgumentID: 2, Values: []int64{10}},
			}}}},
			want: entityOf(apigen.CoreEntityValueOneof{AuthzGrant: &apigen.AuthzGrant{ID: 8, UserID: 4, Grant: apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{Template: &apigen.AuthzTemplateGrant{
				TemplateID: 2,
				Args: []apigen.AuthzArgumentBinding{
					{ArgumentID: 1, Values: apigen.AuthzArgumentValues{Value: apigen.AuthzArgumentValuesValueOneof{Spaces: &apigen.AuthzSpaceValues{Values: []uint64{1, 2}}}}},
					{ArgumentID: 2, Values: apigen.AuthzArgumentValues{Value: apigen.AuthzArgumentValuesValueOneof{EntityRefs: &apigen.AuthzReferenceValues{Values: []apigen.AuthzEntityRef{deploymentRef(10)}}}}},
				},
			}}}}}),
		},
		{
			name: "global allow rule seeded for user visibility",
			t:    apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE,
			old: apigenold.CoreEntity{AuthzGlobalRule: &apigenold.AuthzGlobalRule{ID: 1, Name: "default_user_visibility", Spec: &apigenold.AuthzGlobalRuleSpec{
				DelegationAllowed: true, Permissions: sel(false, 0, []int64{4}, nil), Spaces: sel(false, 0, []int64{0}, nil), EntityTypes: sel(false, 0, []int64{8}, nil), EntityRefs: sel(true, 0, nil, nil),
			}}},
			want: entityOf(apigen.CoreEntityValueOneof{AuthzGlobalRule: &apigen.AuthzGlobalRule{ID: 1, Name: "default_user_visibility", Rule: apigen.AuthzRule{
				Effect:   allow(true),
				Selector: apigen.AuthzSelector{Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_VIEW), Spaces: exactSpaces(0), EntityTypes: exactTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_USER), EntityRefs: refsExcluding()},
			}}}),
		},
		{
			name: "global deny rule with cluster refs",
			t:    apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE,
			old: apigenold.CoreEntity{AuthzGlobalRule: &apigenold.AuthzGlobalRule{ID: 2, Name: "no-agent-settings", Spec: &apigenold.AuthzGlobalRuleSpec{
				Deny: true, DelegatedOnly: true, Permissions: sel(false, 0, []int64{2}, nil), Spaces: sel(true, 0, nil, nil), EntityTypes: sel(false, 0, []int64{7}, nil), EntityRefs: sel(false, 0, []int64{1}, nil),
			}}},
			want: entityOf(apigen.CoreEntityValueOneof{AuthzGlobalRule: &apigen.AuthzGlobalRule{ID: 2, Name: "no-agent-settings", Rule: apigen.AuthzRule{
				Effect:   apigen.AuthzEffect{Value: apigen.AuthzEffectValueOneof{Deny: &apigen.AuthzDeny{DelegatedOnly: true}}},
				Selector: apigen.AuthzSelector{Permissions: exactVerbs(apigen.AuthzVerb_AUTHZ_VERB_UPDATE), Spaces: spacesExcluding(), EntityTypes: exactTypes(apigen.AuthzEntityKind_AUTHZ_ENTITY_KIND_CLUSTER), EntityRefs: exactRefs(systemConfigRef(1))},
			}}}),
		},
		{
			name: "system config with every settings union case",
			t:    apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG,
			old:  apigenold.CoreEntity{SystemConfig: oldSystemConfig()},
			want: entityOf(apigen.CoreEntityValueOneof{SystemConfig: &sysCfg}),
		},
		{
			name: "system config with a master password hash",
			t:    apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG,
			old: func() apigenold.CoreEntity {
				cfg := oldSystemConfig()
				cfg.MasterPasswordHash = "hash"
				return apigenold.CoreEntity{SystemConfig: cfg}
			}(),
			want: entityOf(apigen.CoreEntityValueOneof{SystemConfig: &sysCfgHashed}),
		},
		{
			name: "pending agent session",
			t:    apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION,
			ids:  IDs{EntityID: 11},
			old:  apigenold.CoreEntity{AgentSession: &apigenold.AgentSession{UserID: 4, ID: "sess-1", Status: apigenold.AgentSessionStatus_AGENT_SESSION_PENDING, RequestingAddress: "1.2.3.4", ApprovalCode: "ABCD"}},
			want: entityOf(apigen.CoreEntityValueOneof{AgentSession: &apigen.AgentSession{
				ID: 11, SessionID: "sess-1", UserID: 4, Status: apigen.AgentSessionStatus_AGENT_SESSION_STATUS_PENDING, RequestingAddress: "1.2.3.4", ApprovalCode: apigen.Some("ABCD"),
			}}),
		},
		{
			name: "approved agent session with a collected token",
			t:    apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION,
			ids:  IDs{EntityID: 12},
			old: apigenold.CoreEntity{AgentSession: &apigenold.AgentSession{
				UserID: 4, ID: "sess-2", Status: apigenold.AgentSessionStatus_AGENT_SESSION_APPROVED, ApprovedAt: ms(3000), ExpiresAt: ms(9000), TokenPrefix: "odp_ab", TokenHash: []byte{9, 9},
			}},
			want: entityOf(apigen.CoreEntityValueOneof{AgentSession: &apigen.AgentSession{
				ID: 12, SessionID: "sess-2", UserID: 4, Status: apigen.AgentSessionStatus_AGENT_SESSION_STATUS_APPROVED, ApprovedAt: apigen.Some(ms(3000)),
				Token: apigen.Some(apigen.AgentToken{Hash: apigen.Some([]byte{9, 9}), Prefix: "odp_ab", ExpiresAt: ms(9000)}),
			}}),
		},
		{
			name: "bootstrap user session with a token hash",
			t:    apigen.CoreEntityType_CORE_ENTITY_USER_SESSION,
			ids:  IDs{EntityID: 13},
			old: apigenold.CoreEntity{UserSession: &apigenold.UserSession{
				ID: "us-1", ExpiresAt: ms(9000), UserID: 4, Kind: apigenold.UserSessionKind_USER_SESSION_KIND_BOOTSTRAP, RequestingAddress: "a", UserAgent: "ua", TokenHash: []byte{1},
			}},
			want: entityOf(apigen.CoreEntityValueOneof{UserSession: &apigen.UserSession{
				ID: 13, SessionID: "us-1", UserID: 4, Kind: apigen.UserSessionKind_USER_SESSION_KIND_BOOTSTRAP, ExpiresAt: ms(9000), RequestingAddress: "a", UserAgent: "ua", TokenHash: apigen.Some([]byte{1}),
			}}),
		},
		{
			name: "revoked user session without a token hash",
			t:    apigen.CoreEntityType_CORE_ENTITY_USER_SESSION,
			ids:  IDs{EntityID: 14},
			old:  apigenold.CoreEntity{UserSession: &apigenold.UserSession{ID: "us-2", ExpiresAt: ms(9000), RevokedAt: ms(5000), UserID: 4}},
			want: entityOf(apigen.CoreEntityValueOneof{UserSession: &apigen.UserSession{ID: 14, SessionID: "us-2", UserID: 4, ExpiresAt: ms(9000), RevokedAt: apigen.Some(ms(5000))}}),
		},
		{
			name: "nix store reset drops requested_at",
			t:    apigen.CoreEntityType_CORE_ENTITY_NIX_STORE_RESET,
			ids:  IDs{EntityID: 15},
			old:  apigenold.CoreEntity{NixStoreReset: &apigenold.NixStoreReset{Repo: "github.com/x/y", RequestedAt: 123}},
			want: entityOf(apigen.CoreEntityValueOneof{NixStoreReset: &apigen.NixStoreReset{ID: 15, Repo: "github.com/x/y"}}),
		},
		{
			name: "machine keyslot",
			t:    apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT,
			ids:  IDs{Keyslot: keyslotIDs},
			old:  apigenold.CoreEntity{SecretKeyslot: &apigenold.SecretKeyslot{Kind: apigenold.SecretKeyslotKind_SECRET_KEYSLOT_MACHINE, NodeID: 3, SmkVersion: 1, WrappedSmk: []byte{1}, Nonce: []byte{2}, UpdatedAt: 5}},
			want: entityOf(apigen.CoreEntityValueOneof{SecretKeyslot: &apigen.SecretKeyslot{
				ID: 103, SmkVersion: 1, WrappedSmk: []byte{1}, Nonce: []byte{2},
				Wrapping: apigen.KeyslotWrapping{Value: apigen.KeyslotWrappingValueOneof{MachineKey: &apigen.MachineKey{NodeID: 3}}},
			}}),
		},
		{
			name: "recovery keyslot",
			t:    apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT,
			ids:  IDs{Keyslot: keyslotIDs},
			old:  apigenold.CoreEntity{SecretKeyslot: &apigenold.SecretKeyslot{Kind: apigenold.SecretKeyslotKind_SECRET_KEYSLOT_RECOVERY, SmkVersion: 2, WrappedSmk: []byte{1}, Nonce: []byte{2}, KdfSalt: []byte{7}}},
			want: entityOf(apigen.CoreEntityValueOneof{SecretKeyslot: &apigen.SecretKeyslot{
				ID: 100, SmkVersion: 2, WrappedSmk: []byte{1}, Nonce: []byte{2},
				Wrapping: apigen.KeyslotWrapping{Value: apigen.KeyslotWrappingValueOneof{RecoveryCode: &apigen.RecoveryCode{KdfSalt: []byte{7}}}},
			}}),
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := tc.old
			got, repairs, err := Entity(tc.t, old.Encode(), tc.ids)
			if err != nil {
				t.Fatalf("Entity: %v", err)
			}
			if repairs != nil {
				t.Fatalf("strict mode reported repairs: %q", repairs)
			}
			want := tc.want
			assertSame(t, got, &want)
		})
	}
}

func TestScheduledInstanceState(t *testing.T) {
	instance := apigenold.ScheduledInstance{ID: 5, DeploymentID: 12, NodeID: 1, State: apigenold.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING, DeploymentVersion: 3, DeploymentSpecVersion: 2, SpaceID: 1}
	wantInstance := apigen.ScheduledInstance{ID: 5, NodeID: 1, SpaceID: 1, Deployment: apigen.DeploymentRef{DeploymentID: 12, Version: 3}}
	remote := newRemoteDeployment()
	t.Run("with status", func(t *testing.T) {
		old := &apigenold.ScheduledInstanceState{
			Instance: instance,
			Config: apigenold.DeploymentEvent{
				DeploymentID: 12, Version: 3, Seq: 44, Author: 4, EventType: apigenold.EventType_EVENT_TYPE_UPDATE,
				CreatedTime: ms(100), EventTime: ms(200), SpecVersion: 2, Value: *oldRemoteDeployment(),
			},
			Status: *oldFullStatus(),
		}
		got, err := ScheduledInstanceState(old.Encode())
		if err != nil {
			t.Fatal(err)
		}
		status := newFullStatus()
		assertSame(t, got, &apigen.ScheduledInstanceState{
			Instance: wantInstance,
			Config:   apigen.DeploymentRecord{Deployment: remote, Meta: apigen.EntityMeta{CreatedTime: 100, UpdatedTime: 200, UpdatedSeq: 44, UpdatedActor: 4, Version: 3, SpecVersion: 2}},
			Status:   apigen.Some(status),
		})
	})
	t.Run("deleted record without status", func(t *testing.T) {
		old := &apigenold.ScheduledInstanceState{
			Instance: instance,
			Config: apigenold.DeploymentEvent{
				DeploymentID: 12, Version: 4, Seq: 50, Author: -2, EventType: apigenold.EventType_EVENT_TYPE_DELETE,
				CreatedTime: ms(100), EventTime: ms(300), SpecVersion: 2, Value: *oldRemoteDeployment(),
			},
		}
		got, err := ScheduledInstanceState(old.Encode())
		if err != nil {
			t.Fatal(err)
		}
		assertSame(t, got, &apigen.ScheduledInstanceState{
			Instance: wantInstance,
			Config:   apigen.DeploymentRecord{Deployment: remote, Meta: apigen.EntityMeta{CreatedTime: 100, UpdatedTime: 300, UpdatedSeq: 50, UpdatedActor: -2, Version: 4, SpecVersion: 2, Deleted: true}},
		})
	})
}

func TestClusterBlobs(t *testing.T) {
	t.Run("cluster network info", func(t *testing.T) {
		got, err := ClusterNetworkInfo((&apigenold.ClusterNetworkInfo{UlaPrefix: []byte{0xfd, 1, 2, 3, 4, 5}}).Encode())
		if err != nil {
			t.Fatal(err)
		}
		assertSame(t, got, &apigen.ClusterNetworkInfo{UlaPrefix: []byte{0xfd, 1, 2, 3, 4, 5}})
	})
	t.Run("cluster net map", func(t *testing.T) {
		old := &apigenold.ClusterNetMap{
			TargetNodeID: 2, UlaPrefix: []byte{0xfd, 1, 2, 3, 4, 5}, DerivedFromSeq: 99,
			Nodes: []*apigenold.ClusterNetMapNode{{
				NodeID: 1, UnderlayAddress: "10.0.0.1", WgPublicKey: "pk", WgListenPort: 51820,
				IngressPublish: []*apigenold.IngressPublish{{Address: "10.0.0.1", Port: 443}},
			}},
			Routes:      []*apigenold.ClusterNetMapRoute{{LogicalPrefix: "fd01:2:3::/64", HostingNodeID: 1}},
			PolicyRules: []*apigenold.NetPolicyRule{{Source: &apigenold.NetPolicyPeer{SpaceID: 1}, Destination: &apigenold.NetPolicyPeer{SpaceID: 1, DeploymentID: 10}, Ports: []*apigenold.NetPortMatch{{Protocol: apigenold.NetProtocol_NET_PROTOCOL_TCP, Port: 443}}}},
			DnsServices: []*apigenold.ClusterNetMapService{{Name: "web", SpaceID: 1, DeploymentID: 10, Ordinals: []*apigenold.ClusterNetMapServiceOrdinal{{Ordinal: 0}, {Ordinal: 1}}}},
		}
		got, err := ClusterNetMap(old.Encode())
		if err != nil {
			t.Fatal(err)
		}
		assertSame(t, got, &apigen.ClusterNetMap{
			TargetNodeID: 2, UlaPrefix: []byte{0xfd, 1, 2, 3, 4, 5}, DerivedFromSeq: 99,
			Nodes:       []apigen.ClusterNetMapNode{{NodeID: 1, UnderlayAddress: "10.0.0.1", WgPublicKey: "pk", WgListenPort: 51820, IngressPublish: []apigen.IngressPublish{{Address: "10.0.0.1", Port: 443}}}},
			Routes:      []apigen.ClusterNetMapRoute{{LogicalPrefix: "fd01:2:3::/64", HostingNodeID: 1}},
			PolicyRules: []apigen.NetPolicyRule{{Source: apigen.NetPolicyPeer{SpaceID: 1}, Destination: apigen.NetPolicyPeer{SpaceID: 1, DeploymentID: 10}, Ports: []apigen.NetPortMatch{{Protocol: apigen.NetProtocol_NET_PROTOCOL_TCP, Range: apigen.PortRange{Start: 443, End: 443}}}}},
			DnsServices: []apigen.ClusterNetMapService{{Name: "web", SpaceID: 1, DeploymentID: 10, Ordinals: []apigen.ClusterNetMapServiceOrdinal{{Ordinal: 0}, {Ordinal: 1}}}},
		})
	})
}

func TestRunnerStatusExtra(t *testing.T) {
	cases := []struct {
		name string
		old  []byte
		want string
	}{
		{name: "empty blob", old: nil, want: ""},
		{name: "no diagnostics", old: (&apigenold.RunnerStatus{Status: apigenold.RunningStatus_RUNNING}).Encode(), want: ""},
		{name: "diagnostics", old: (&apigenold.RunnerStatus{NetworkDiagnostics: []string{"a", "b"}}).Encode(), want: `["a","b"]`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := RunnerStatusExtra(tc.old)
			if err != nil {
				t.Fatal(err)
			}
			if string(got) != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
