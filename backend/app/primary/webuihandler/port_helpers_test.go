package webuihandler

import (
	"hash/fnv"
	"net/netip"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/authz"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state"
	"github.com/jptrs93/opsagent/backend/storage/primarydb/state/statetest"
)

func ensureTestNode(store *state.Service, name, identifier string) *nodes.Node {
	h := fnv.New32a()
	h.Write([]byte(identifier))
	sum := h.Sum32()
	addr := netip.AddrFrom4([4]byte{10, byte(sum >> 16), byte(sum >> 8), byte(sum)})
	return nodes.EnsurePrimaryNode(store, name, identifier, addr, "")
}

func allowEffect(delegationAllowed bool) apigen.AuthzEffect {
	return apigen.AuthzEffect{Value: apigen.AuthzEffectValueOneof{Allow: &apigen.AuthzAllow{DelegationAllowed: delegationAllowed}}}
}

func denyEffect(delegatedOnly bool) apigen.AuthzEffect {
	return apigen.AuthzEffect{Value: apigen.AuthzEffectValueOneof{Deny: &apigen.AuthzDeny{DelegatedOnly: delegatedOnly}}}
}

func allVerbsExcluding(vs ...apigen.AuthzVerb) apigen.AuthzPermissionSelector {
	return apigen.AuthzPermissionSelector{AllVerbsExcluding: apigen.Some(apigen.AuthzVerbList{Values: vs})}
}

func exactVerbs(vs ...apigen.AuthzVerb) apigen.AuthzPermissionSelector {
	return apigen.AuthzPermissionSelector{ExactVerbs: apigen.Some(apigen.AuthzVerbList{Values: vs})}
}

func allSpacesExcluding(ids ...uint64) apigen.AuthzSpaceSelector {
	return apigen.AuthzSpaceSelector{AllSpacesExcluding: apigen.Some(apigen.SpaceIdList{Values: ids})}
}

func exactSpaces(ids ...uint64) apigen.AuthzSpaceSelector {
	return apigen.AuthzSpaceSelector{ExactSpaces: apigen.Some(apigen.SpaceIdList{Values: ids})}
}

func allEntityTypesExcluding(ks ...apigen.AuthzEntityKind) apigen.AuthzEntityTypeSelector {
	return apigen.AuthzEntityTypeSelector{AllEntityTypesExcluding: apigen.Some(apigen.AuthzEntityKindList{Values: ks})}
}

func exactEntityTypes(ks ...apigen.AuthzEntityKind) apigen.AuthzEntityTypeSelector {
	return apigen.AuthzEntityTypeSelector{ExactEntityTypes: apigen.Some(apigen.AuthzEntityKindList{Values: ks})}
}

func allEntityRefs() apigen.AuthzEntityRefSelector {
	return apigen.AuthzEntityRefSelector{AllEntityRefsExcluding: apigen.Some(apigen.AuthzEntityRefList{})}
}

func exactRefs(refs ...apigen.AuthzEntityRef) apigen.AuthzEntityRefSelector {
	return apigen.AuthzEntityRefSelector{ExactEntityRefs: apigen.Some(apigen.AuthzEntityRefList{Values: refs})}
}

func anySelector() apigen.AuthzSelector {
	return apigen.AuthzSelector{
		Permissions: allVerbsExcluding(),
		Spaces:      allSpacesExcluding(),
		EntityTypes: allEntityTypesExcluding(),
		EntityRefs:  allEntityRefs(),
	}
}

func allowRule(sel apigen.AuthzSelector) *apigen.AuthzRule {
	return &apigen.AuthzRule{Effect: allowEffect(true), Selector: sel}
}

func denyRule(sel apigen.AuthzSelector) *apigen.AuthzRule {
	return &apigen.AuthzRule{Effect: denyEffect(false), Selector: sel}
}

func deploymentRef(id uint64) apigen.AuthzEntityRef {
	return apigen.AuthzEntityRef{Target: apigen.AuthzEntityRefTarget{Value: apigen.AuthzEntityRefTargetValueOneof{Deployment: &id}}}
}

func secretRef(id uint64) apigen.AuthzEntityRef {
	return apigen.AuthzEntityRef{Target: apigen.AuthzEntityRefTarget{Value: apigen.AuthzEntityRefTargetValueOneof{Secret: &id}}}
}

func spaceBinding(argID uint32, ids ...uint64) apigen.AuthzArgumentBinding {
	return apigen.AuthzArgumentBinding{
		ArgumentID: argID,
		Values:     apigen.AuthzArgumentValues{Value: apigen.AuthzArgumentValuesValueOneof{Spaces: &apigen.AuthzSpaceValues{Values: ids}}},
	}
}

func templateSource(templateID uint64, args ...apigen.AuthzArgumentBinding) apigen.AuthzGrantSource {
	return apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{Template: &apigen.AuthzTemplateGrant{TemplateID: templateID, Args: args}}}
}

func templateGrant(userID, templateID uint64, args ...apigen.AuthzArgumentBinding) *apigen.AuthzGrant {
	return &apigen.AuthzGrant{UserID: userID, Grant: templateSource(templateID, args...)}
}

func clusterAdminGrant(userID uint64) *apigen.AuthzGrant {
	return templateGrant(userID, authz.ClusterAdminTemplateID)
}

func ruleGrant(userID uint64, rule *apigen.AuthzRule) *apigen.AuthzGrant {
	return &apigen.AuthzGrant{UserID: userID, Grant: apigen.AuthzGrantSource{Value: apigen.AuthzGrantSourceValueOneof{Rule: rule}}}
}

func remoteContainer(image string) apigen.Workload {
	return apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
		Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{RemoteImage: &apigen.RemoteImage{Image: image}}},
		UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
	}}}
}

func nixContainer(repo, flake, version string) apigen.Workload {
	return apigen.Workload{Value: apigen.WorkloadValueOneof{Container: &apigen.ContainerSpec{
		Source:          apigen.ContainerSource{Value: apigen.ContainerSourceValueOneof{NixImageBuild: &apigen.NixImageBuild{Repo: repo, Flake: flake}}},
		Version:         version,
		UpgradeStrategy: apigen.ContainerUpgradeStrategy_CONTAINER_UPGRADE_STRATEGY_RECREATE,
	}}}
}

func literalEnv(value string) apigen.EnvVar {
	return apigen.EnvVar{Value: apigen.EnvVarValueOneof{Literal: &apigen.LiteralEnv{Value: value}}}
}

func addressEnv(deploymentID, spaceID uint64) apigen.EnvVar {
	return apigen.EnvVar{Value: apigen.EnvVarValueOneof{Address: &apigen.AddressEnv{DeploymentID: deploymentID, SpaceID: spaceID}}}
}

func secretEnv(ref apigen.ValueRef) apigen.EnvVar {
	return apigen.EnvVar{Value: apigen.EnvVarValueOneof{Secret: &apigen.SecretEnv{Secret: apigen.SecretRef{SecretID: ref.ID, Version: ref.Version}}}}
}

func configEnv(ref apigen.ValueRef) apigen.EnvVar {
	return apigen.EnvVar{Value: apigen.EnvVarValueOneof{Config: &apigen.ConfigEnv{Config: apigen.ConfigRef{ConfigID: ref.ID, Version: ref.Version}}}}
}

func tlsPassthroughIngress(hostname string, containerPort uint32, listen ...apigen.IngressListen) apigen.Ingress {
	return apigen.Ingress{
		Hostname: hostname,
		Listen:   listen,
		Config:   apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{TlsPassthrough: &apigen.TlsPassthroughConfig{ContainerPort: containerPort}}},
	}
}

func mustPrefixes(values ...string) []apigen.IpPrefix {
	out := make([]apigen.IpPrefix, 0, len(values))
	for _, v := range values {
		pfx, err := apigen.ParsePrefix(v)
		if err != nil {
			addr, aerr := netip.ParseAddr(v)
			if aerr != nil {
				panic(err)
			}
			pfx = apigen.PrefixOf(netip.PrefixFrom(addr, addr.BitLen()))
		}
		out = append(out, pfx)
	}
	return out
}

func optID(id uint64) apigen.Maybe[uint64] {
	if id == 0 {
		return apigen.Maybe[uint64]{}
	}
	return apigen.Some(id)
}

func idOf(m apigen.Maybe[uint64]) uint64 {
	if !m.Present {
		return 0
	}
	return m.Value
}

func tlsPassthroughIngressOnPort(hostname string, containerPort, hostPort uint32) apigen.Ingress {
	ing := tlsPassthroughIngress(hostname, containerPort)
	ing.Config.Value.TlsPassthrough.HostPort = apigen.Some(hostPort)
	return ing
}

func httpsIngress(hostname string, containerPort uint32, cert apigen.Maybe[apigen.CertSource]) apigen.Ingress {
	return apigen.Ingress{
		Hostname: hostname,
		Config:   apigen.IngressConfig{Value: apigen.IngressConfigValueOneof{Https: &apigen.HttpsConfig{ContainerPort: containerPort, BackendProtocol: apigen.HttpBackendProtocol_HTTP_BACKEND_PROTOCOL_HTTP1, CertSource: cert}}},
	}
}

func secretCertSource(ref apigen.ValueRef) apigen.Maybe[apigen.CertSource] {
	return apigen.Some(apigen.CertSource{Value: apigen.CertSourceValueOneof{Secret: &apigen.SecretCertSource{Secret: secretRefOf(ref)}}})
}

func secretRefOf(ref apigen.ValueRef) apigen.SecretRef {
	return apigen.SecretRef{SecretID: ref.ID, Version: ref.Version}
}

func assetRefOf(ref apigen.ValueRef) apigen.AssetRef {
	return apigen.AssetRef{AssetID: ref.ID, Version: ref.Version}
}

func mustAddr(s string) apigen.IpAddress {
	return apigen.AddrOf(netip.MustParseAddr(s))
}

func foldEntities(entries []apigen.MaterialisedEntity) map[apigen.CoreEntityType]map[uint64]*apigen.CoreEntity {
	ptrs := make([]*apigen.MaterialisedEntity, len(entries))
	for i := range entries {
		ptrs[i] = &entries[i]
	}
	return statetest.FoldSnapshot(ptrs)
}

func foldEvents(events []apigen.CoreWriteUpdate) map[apigen.CoreEntityType]map[uint64]*apigen.CoreEntity {
	ptrs := make([]*apigen.CoreWriteUpdate, len(events))
	for i := range events {
		ptrs[i] = &events[i]
	}
	return statetest.Fold(ptrs)
}

func testConfigService(t *testing.T, store *state.Service) *systemconfig.Service {
	t.Helper()
	configService, err := systemconfig.InitializeService(store, *systemconfig.Default(systemconfig.DefaultInitial()))
	if err != nil {
		t.Fatalf("systemconfig.InitializeService: %v", err)
	}
	return configService
}
