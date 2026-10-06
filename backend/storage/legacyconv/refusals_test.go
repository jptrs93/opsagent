package legacyconv

import (
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/apigenold"
)

func container(spec apigenold.DeploymentSpec) *apigenold.Deployment {
	return &apigenold.Deployment{ID: 1, Name: "d", SpaceID: 1, Scheduling: apigenold.Scheduling{DedicatedNodes: &apigenold.DedicatedNodesScheduling{Nodes: []int32{1}}}, Spec: spec}
}

func remoteSpec() *apigenold.ContainerSpec {
	return &apigenold.ContainerSpec{Source: apigenold.ContainerBundleSource{RemoteImage: &apigenold.RemoteDockerImage{Image: "img"}}}
}

func TestEntityRefusals(t *testing.T) {
	literal := "v"
	okSel := sel(true, 0, nil, nil)
	globalRule := func(permissions, spaces, types, refs *apigenold.AuthzSelector) apigenold.CoreEntity {
		return apigenold.CoreEntity{AuthzGlobalRule: &apigenold.AuthzGlobalRule{ID: 1, Name: "r", Spec: &apigenold.AuthzGlobalRuleSpec{Permissions: permissions, Spaces: spaces, EntityTypes: types, EntityRefs: refs}}}
	}
	template := func(args []*apigenold.AuthzTemplateArgument, rules ...*apigenold.AuthzRule) apigenold.CoreEntity {
		return apigenold.CoreEntity{AuthzRuleTemplate: &apigenold.AuthzRuleTemplate{ID: 3, Name: "t", Spec: &apigenold.AuthzRuleTemplateSpec{Arguments: args, Rules: rules}}}
	}
	tpl := newTemplate()
	cases := []struct {
		name string
		t    apigen.CoreEntityType
		old  apigenold.CoreEntity
		ids  IDs
		want string
	}{
		{"entity type does not match the payload", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Space: &apigenold.Space{ID: 1, Name: "s"}}, IDs{}, "does not carry"},
		{"grant without a template lookup", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, apigenold.CoreEntity{AuthzGrant: &apigenold.AuthzGrant{ID: 1, UserID: 1, TemplateID: 2}}, IDs{}, "IDs.Template"},
		{"keyslot without an id mapping", apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, apigenold.CoreEntity{SecretKeyslot: &apigenold.SecretKeyslot{Kind: apigenold.SecretKeyslotKind_SECRET_KEYSLOT_RECOVERY}}, IDs{}, "IDs.Keyslot"},
		{"second container spec", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: remoteSpec(), Container2Spec: remoteSpec()})}, IDs{}, "container2_spec"},
		{"no workload", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{})}, IDs{}, "container1_spec"},
		{"both sources", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: &apigenold.ContainerSpec{Source: apigenold.ContainerBundleSource{RemoteImage: &apigenold.RemoteDockerImage{Image: "i"}, NixDockerBuild: &apigenold.NixDockerBuild{Repo: "r"}}}})}, IDs{}, "both nix_docker_build and remote_image"},
		{"env var with two arms", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: &apigenold.ContainerSpec{Source: remoteSpec().Source, Runtime: apigenold.ContainerRuntime{EnvVars: map[string]*apigenold.EnvVarValue{"X": {Value: &literal, Secret: vref(1, 1)}}}}})}, IDs{}, "alternatives set"},
		{"asset display key without a ref", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: &apigenold.ContainerSpec{Source: remoteSpec().Source, Runtime: apigenold.ContainerRuntime{EnvVars: map[string]*apigenold.EnvVarValue{"X": {Asset: "logo"}}}}})}, IDs{}, "without an asset_ref"},
		{"address env with one half", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: &apigenold.ContainerSpec{Source: remoteSpec().Source, Runtime: apigenold.ContainerRuntime{EnvVars: map[string]*apigenold.EnvVarValue{"X": {AddressDeploymentID: i32p(1)}}}}})}, IDs{}, "set together"},
		{"unplaced deployment", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: &apigenold.Deployment{ID: 1, Name: "d", SpaceID: 1, Spec: apigenold.DeploymentSpec{Container1Spec: remoteSpec()}}}, IDs{}, "dedicated_nodes"},
		{"opendeploy spec with ingress", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{OpendeploySpec: &apigenold.OpendeploySpec{Version: "v"}, Networking: apigenold.NetworkingConfig{Ingress: []*apigenold.Ingress{{Kind: apigenold.IngressKind_INGRESS_KIND_HTTPS}}}})}, IDs{}, "self-spec"},
		{"ingress without a kind", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: remoteSpec(), Networking: apigenold.NetworkingConfig{Ingress: []*apigenold.Ingress{{Hostname: "h", HttpsConfig: &apigenold.HttpsConfig{ContainerPort: 80}}}}})}, IDs{}, "Ingress.kind"},
		{"ingress whose config is missing", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: remoteSpec(), Networking: apigenold.NetworkingConfig{Ingress: []*apigenold.Ingress{{Hostname: "h", Kind: apigenold.IngressKind_INGRESS_KIND_TLS_PASSTHROUGH}}}})}, IDs{}, "tls_passthrough_config"},
		{"unparsable ip filter entry", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: remoteSpec(), Networking: apigenold.NetworkingConfig{PortForwarding: []*apigenold.PortForward{{Protocol: apigenold.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, HostPort: 1, ContainerPort: 1, IpFilter: &apigenold.IpFilter{Allow: []string{"nope"}}}}}})}, IDs{}, "not an IP address or CIDR prefix"},
		{"mount without a permission fails validation", apigen.CoreEntityType_CORE_ENTITY_DEPLOYMENT, apigenold.CoreEntity{Deployment: container(apigenold.DeploymentSpec{Container1Spec: &apigenold.ContainerSpec{Source: remoteSpec().Source, Runtime: apigenold.ContainerRuntime{Mounts: []*apigenold.CustomHostMount{{HostPath: "/h", ContainerPath: "/c"}}}}})}, IDs{}, "permission"},
		{"node with an empty underlay address", apigen.CoreEntityType_CORE_ENTITY_NODE, apigenold.CoreEntity{Node: &apigenold.Node{ID: 1, Status: apigenold.NodeLifecycleStatus_NODE_MEMBER_NORMAL}}, IDs{}, "underlay_address"},
		{"asset with a short digest", apigen.CoreEntityType_CORE_ENTITY_ASSET, apigenold.CoreEntity{Asset: &apigenold.Asset{ID: 1, Fs: &apigenold.AssetFs{Key: "k"}, SpaceID: 1, Sha256: "abcd", StorageKey: "s"}}, IDs{}, "sha256"},
		{"machine keyslot with a salt", apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, apigenold.CoreEntity{SecretKeyslot: &apigenold.SecretKeyslot{Kind: apigenold.SecretKeyslotKind_SECRET_KEYSLOT_MACHINE, NodeID: 1, SmkVersion: 1, WrappedSmk: []byte{1}, Nonce: []byte{1}, KdfSalt: []byte{1}}}, IDs{Keyslot: func(apigenold.SecretKeyslot) uint64 { return 9 }}, "kdf_salt"},
		{"keyslot without a kind", apigen.CoreEntityType_CORE_ENTITY_SECRET_KEYSLOT, apigenold.CoreEntity{SecretKeyslot: &apigenold.SecretKeyslot{SmkVersion: 1, WrappedSmk: []byte{1}, Nonce: []byte{1}}}, IDs{Keyslot: func(apigenold.SecretKeyslot) uint64 { return 9 }}, "SecretKeyslot.kind"},
		{"deny network policy", apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, apigenold.CoreEntity{NetworkPolicy: &apigenold.NetworkPolicy{ID: 1, Action: apigenold.NetworkPolicyAction_NETWORK_POLICY_ACTION_DENY, Source: &apigenold.NetworkPolicyPeerRef{Kind: apigenold.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1}, Destination: &apigenold.NetworkPolicyPeerRef{Kind: apigenold.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1}}}, IDs{}, "deny"},
		{"policy peer without a kind", apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, apigenold.CoreEntity{NetworkPolicy: &apigenold.NetworkPolicy{ID: 1, Action: apigenold.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW, Source: &apigenold.NetworkPolicyPeerRef{ID: 1}, Destination: &apigenold.NetworkPolicyPeerRef{Kind: apigenold.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1}}}, IDs{}, "source.kind"},
		{"policy port zero fails validation", apigen.CoreEntityType_CORE_ENTITY_NETWORK_POLICY, apigenold.CoreEntity{NetworkPolicy: &apigenold.NetworkPolicy{ID: 1, Action: apigenold.NetworkPolicyAction_NETWORK_POLICY_ACTION_ALLOW, Source: &apigenold.NetworkPolicyPeerRef{Kind: apigenold.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1}, Destination: &apigenold.NetworkPolicyPeerRef{Kind: apigenold.NetworkPolicyPeerKind_NETWORK_POLICY_PEER_KIND_SPACE, ID: 1}, Ports: []*apigenold.NetPortMatch{{Protocol: apigenold.NetProtocol_NET_PROTOCOL_TCP}}}}, IDs{}, "start"},
		{"unset selector", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, globalRule(okSel, nil, okSel, okSel), IDs{}, "spaces: selector is unset"},
		{"include emptied by its exclusions", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, globalRule(sel(false, 0, []int64{4}, []int64{4}), okSel, okSel, okSel), IDs{}, "matches nothing"},
		{"argument outside a template", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, globalRule(okSel, sel(false, 1, nil, nil), okSel, okSel), IDs{}, "outside a template"},
		{"entity refs under a wildcard type position", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, globalRule(okSel, okSel, okSel, sel(false, 0, []int64{1}, nil)), IDs{}, "exactly one kind"},
		{"entity refs under the access kind", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GLOBAL_RULE, globalRule(okSel, okSel, sel(false, 0, []int64{9}, nil), sel(true, 0, nil, []int64{1})), IDs{}, "ACCESS"},
		{"argument combined with lists", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE, template([]*apigenold.AuthzTemplateArgument{{ID: 1, Name: "a"}}, &apigenold.AuthzRule{Permissions: okSel, Spaces: sel(true, 1, nil, nil), EntityTypes: okSel, EntityRefs: okSel}), IDs{}, "combined with wildcard"},
		{"unused template argument", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE, template([]*apigenold.AuthzTemplateArgument{{ID: 1, Name: "a"}}, &apigenold.AuthzRule{Permissions: okSel, Spaces: okSel, EntityTypes: okSel, EntityRefs: okSel}), IDs{}, "used by no rule"},
		{"argument used with two kinds", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT_TEMPLATE, template([]*apigenold.AuthzTemplateArgument{{ID: 1, Name: "a"}}, &apigenold.AuthzRule{Permissions: sel(false, 1, nil, nil), Spaces: sel(false, 1, nil, nil), EntityTypes: okSel, EntityRefs: okSel}), IDs{}, "is used as kind"},
		{"grant naming an unknown template", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, apigenold.CoreEntity{AuthzGrant: &apigenold.AuthzGrant{ID: 1, UserID: 1, TemplateID: 9}}, IDs{Template: templates(tpl)}, "is unknown"},
		{"grant binding an unknown argument", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, apigenold.CoreEntity{AuthzGrant: &apigenold.AuthzGrant{ID: 1, UserID: 1, TemplateID: 2, Spec: &apigenold.AuthzGrantSpec{Args: []*apigenold.AuthzArgumentBinding{{ArgumentID: 5, Values: []int64{1}}}}}}, IDs{Template: templates(tpl)}, "has no argument 5"},
		{"grant with neither rule nor template", apigen.CoreEntityType_CORE_ENTITY_AUTHZ_GRANT, apigenold.CoreEntity{AuthzGrant: &apigenold.AuthzGrant{ID: 1, UserID: 1}}, IDs{Template: templates(tpl)}, "neither a rule nor a template"},
		{"system config without a ula prefix fails validation", apigen.CoreEntityType_CORE_ENTITY_SYSTEM_CONFIG, apigenold.CoreEntity{SystemConfig: &apigenold.SystemConfig{}}, IDs{}, "network_ula_prefix"},
		{"status without an observation time fails validation", apigen.CoreEntityType_CORE_ENTITY_SCHEDULED_INSTANCE_STATUS, apigenold.CoreEntity{ScheduledInstanceStatus: &apigenold.ScheduledInstanceStatus{ScheduledInstanceID: 1}}, IDs{}, "updated_at"},
		{"agent session without a status fails validation", apigen.CoreEntityType_CORE_ENTITY_AGENT_SESSION, apigenold.CoreEntity{AgentSession: &apigenold.AgentSession{ID: "s", UserID: 1}}, IDs{EntityID: 1}, "status"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := tc.old
			got, _, err := Entity(tc.t, old.Encode(), tc.ids)
			if err == nil {
				t.Fatalf("expected a refusal containing %q, got %+v", tc.want, got)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error %q does not mention %q", err, tc.want)
			}
		})
	}
}

func TestBlobRefusals(t *testing.T) {
	if _, err := ClusterNetworkInfo((&apigenold.ClusterNetworkInfo{UlaPrefix: []byte{1}}).Encode()); err == nil || !strings.Contains(err.Error(), "ula_prefix") {
		t.Fatalf("short ula prefix: %v", err)
	}
	if _, err := ClusterNetMap((&apigenold.ClusterNetMap{PolicyRules: []*apigenold.NetPolicyRule{{Source: &apigenold.NetPolicyPeer{}}}}).Encode()); err == nil || !strings.Contains(err.Error(), "destination") {
		t.Fatalf("missing peer: %v", err)
	}
	if _, err := ScheduledInstanceState((&apigenold.ScheduledInstanceState{Instance: apigenold.ScheduledInstance{ID: 1, DeploymentVersion: 1}, Config: apigenold.DeploymentEvent{Value: apigenold.Deployment{Name: "d", Spec: apigenold.DeploymentSpec{Container1Spec: remoteSpec()}}}}).Encode()); err == nil || !strings.Contains(err.Error(), "dedicated_nodes") {
		t.Fatalf("unplaced pinned record: %v", err)
	}
	if _, err := RunnerStatusExtra([]byte{0xff, 0xff}); err == nil {
		t.Fatal("expected a decode error for garbage extra bytes")
	}
}
