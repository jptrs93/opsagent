package webuihandler

import (
	"context"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/deployments"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/nodes"
	"github.com/jptrs93/opsagent/backend/app/primary/domain/systemconfig"
	"github.com/jptrs93/opsagent/backend/lib/network"
)

func requireClaimRejected(t *testing.T, err error, want string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("err = %v, want a rejection containing %q", err, want)
	}
}

func TestPortForwardCollisionRejectedAtWriteTime(t *testing.T) {
	tcp, udp := apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP
	h, staging := newEnforcementTestHandler(t)
	worker := ensureTestNode(h.Store, "worker", "worker")
	other := ensureTestNode(h.Store, "other", "other")
	create := func(ctx apigen.Context, spaceID uint64, name string, nodeID uint64, spec apigen.DeploymentSpec) (*apigen.DeploymentRecord, error) {
		return h.deploymentsCreate(ctx, &apigen.DeploymentCreateRequest{Name: name, SpaceID: spaceID, Scheduling: apigen.DedicatedScheduling(false, nodeID), Spec: spec})
	}
	admin, operator := enforceCtx(1, false), enforceCtx(2, false)
	first, err := create(admin, staging.ID, "first", worker.ID, forwardSpec(tcp, 18080))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := create(admin, staging.ID, "first_udp", worker.ID, forwardSpec(udp, 15353)); err != nil {
		t.Fatal(err)
	}

	_, err = create(operator, nodes.DefaultSpaceID, "second", worker.ID, forwardSpec(tcp, 18080))
	requireClaimRejected(t, err, "TCP host port 18080 is already forwarded by another deployment on this node")
	_, err = create(operator, nodes.DefaultSpaceID, "second_udp", worker.ID, forwardSpec(udp, 15353))
	requireClaimRejected(t, err, "UDP host port 15353 is already forwarded by another deployment on this node")

	if _, err := create(operator, nodes.DefaultSpaceID, "other_protocol", worker.ID, forwardSpec(udp, 18080)); err != nil {
		t.Fatalf("UDP on a port forwarded over TCP: %v", err)
	}
	if _, err := create(operator, nodes.DefaultSpaceID, "other_node", other.ID, forwardSpec(tcp, 18080)); err != nil {
		t.Fatalf("the same port on another node: %v", err)
	}
	free, err := create(operator, nodes.DefaultSpaceID, "free", worker.ID, forwardSpec(tcp, 18081))
	if err != nil {
		t.Fatal(err)
	}
	_, err = h.deploymentsUpdate(operator, &apigen.DeploymentUpdateRequest{DeploymentID: free.Deployment.ID, ExpectedSeq: free.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{Spec: &apigen.SpecUpdate{Spec: forwardSpec(tcp, 18080)}}})
	requireClaimRejected(t, err, "TCP host port 18080 is already forwarded by another deployment on this node")
	if _, err := h.deploymentsUpdate(admin, &apigen.DeploymentUpdateRequest{DeploymentID: first.Deployment.ID, ExpectedSeq: first.Meta.UpdatedSeq,
		Update: apigen.DeploymentUpdateRequestUpdateOneof{VersionOnly: &apigen.VersionOnlyUpdate{TargetVersion: "1.30"}}}); err != nil {
		t.Fatalf("the holder of an uncontested forward can still update: %v", err)
	}
}

func TestPortForwardOnPlatformPortRejectedAtWriteTime(t *testing.T) {
	tcp, udp := apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP, apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_UDP
	h, _ := newEnforcementTestHandler(t)
	primary := ensureTestNode(h.Store, "primary", "primary")
	worker := ensureTestNode(h.Store, "worker", "worker")
	h.NodeID = primary.ID
	settings := h.SystemConfig.Snapshot().Settings
	_, clusterPort, ok := strings.Cut(h.SystemConfig.MustLoadStringSetting(settings.Cluster.Listen), ":")
	if !ok || clusterPort == "" {
		t.Fatalf("default cluster listen %q has no port", h.SystemConfig.MustLoadStringSetting(settings.Cluster.Listen))
	}
	var cluster uint32
	for _, c := range clusterPort {
		cluster = cluster*10 + uint32(c-'0')
	}
	admin := enforceCtx(1, false)
	create := func(name string, nodeID uint64, spec apigen.DeploymentSpec) error {
		_, err := h.deploymentsCreate(admin, &apigen.DeploymentCreateRequest{Name: name, SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, nodeID), Spec: spec})
		return err
	}
	requireClaimRejected(t, create("wg_primary", primary.ID, forwardSpec(udp, uint32(network.DefaultWGListenPort))), "reserved by the WireGuard cluster transport")
	requireClaimRejected(t, create("wg_worker", worker.ID, forwardSpec(udp, uint32(network.DefaultWGListenPort))), "reserved by the WireGuard cluster transport")
	requireClaimRejected(t, create("cluster", primary.ID, forwardSpec(tcp, cluster)), "reserved by the cluster listener")
	requireClaimRejected(t, create("cluster_passthrough", primary.ID, passthroughHostPortSpec(apigen.Some(cluster))), "reserved by the cluster listener")
	if err := create("cluster_port_on_worker", worker.ID, forwardSpec(tcp, cluster)); err != nil {
		t.Fatalf("the primary's cluster port on a worker: %v", err)
	}
	if err := create("wg_port_over_tcp", worker.ID, forwardSpec(tcp, uint32(network.DefaultWGListenPort))); err != nil {
		t.Fatalf("the WireGuard port over TCP: %v", err)
	}
}

func TestSettingsChangeRejectedWhenListenerMovesOntoAForward(t *testing.T) {
	tcp := apigen.PortForwardProtocol_PORT_FORWARD_PROTOCOL_TCP
	h, _ := newEnforcementTestHandler(t)
	primary := ensureTestNode(h.Store, "primary", "primary")
	h.NodeID = primary.ID
	if _, err := h.deploymentsCreate(enforceCtx(1, false), &apigen.DeploymentCreateRequest{Name: "db", SpaceID: nodes.DefaultSpaceID, Scheduling: apigen.DedicatedScheduling(false, primary.ID), Spec: forwardSpec(tcp, 15432)}); err != nil {
		t.Fatal(err)
	}
	settings := func(listen string) *apigen.ClusterSettings {
		return &apigen.ClusterSettings{HttpsWeb: apigen.HttpsWebSettings{Enabled: systemconfig.BoolLiteral(true), Listen: systemconfig.StringLiteral(listen)}}
	}
	if err := deployments.ValidateIngressAgainstSettings(context.Background(), h.Store.Queries(), primary.ID, settings(":8443")); err != nil {
		t.Fatalf("a Web UI port nothing forwards: %v", err)
	}
	err := deployments.ValidateIngressAgainstSettings(context.Background(), h.Store.Queries(), primary.ID, settings(":15432"))
	if err == nil || !strings.Contains(err.Error(), `deployment "db"`) || !strings.Contains(err.Error(), "collides with the primary Web UI") {
		t.Fatalf("moving the Web UI onto a forwarded port must be rejected, got %v", err)
	}
}
