//go:build linux

package network

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vishvananda/netlink"
)

// These tests build real network namespaces, veths and nftables state, so they
// only run as root with OPENDEPLOY_NETNS_TESTS set, in a disposable machine or
// a privileged container.
func requireNetnsTests(t *testing.T) {
	t.Helper()
	if os.Getenv("OPENDEPLOY_NETNS_TESTS") == "" || os.Geteuid() != 0 {
		t.Skip("set OPENDEPLOY_NETNS_TESTS and run as root to exercise real network namespaces")
	}
}

// An agent restart during a rollover reattaches two placements of one
// deployment. The first to recover used to sweep the deployment and delete the
// other's namespace, because nothing is tracked until each runner has recovered.
func TestCleanupContainerNetsLeavesContainersInUse(t *testing.T) {
	requireNetnsTests(t)
	const deploymentID = 7001
	prefix := GeneratePrefix()
	spec := func(containerID string, instanceID int32) ContainerNetSpec {
		return ContainerNetSpec{
			ContainerID:  containerID,
			DeploymentID: deploymentID,
			InboundAddr:  mustAddr(prefix.InboundAddr(1, deploymentID, 0)),
			OutboundAddr: mustAddr(prefix.OutboundAddr(1, deploymentID, 0, instanceID, 1)),
		}
	}
	exists := func(cn *ContainerNet) bool {
		_, nsErr := os.Stat(filepath.Join(netnsRunDir, cn.ContainerID))
		_, linkErr := netlink.LinkByName(cn.HostVeth)
		return nsErr == nil && linkErr == nil
	}

	before := New(prefix, 0)
	t.Cleanup(func() { New(prefix, 0).CleanupContainerNets(deploymentID, nil) })
	servingSpec := spec("opendeploy-7001-149-1180-1", 1180)
	serving, err := before.SetupContainerNet(servingSpec)
	if err != nil {
		t.Fatal(err)
	}
	candidateSpec := spec("opendeploy-7001-150-1200-1", 1200)
	candidate, err := before.SetupContainerNet(candidateSpec)
	if err != nil {
		t.Fatal(err)
	}

	// The restarted agent tracks nothing until each runner recovers its own.
	after := New(prefix, 0)
	if _, err := after.RecoverContainerNet(serving.ContainerID, deploymentID, servingSpec.InboundAddr, servingSpec.OutboundAddr); err != nil {
		t.Fatal(err)
	}
	after.CleanupContainerNets(deploymentID, []string{candidate.ContainerID})
	if !exists(candidate) {
		t.Fatal("the sweep removed the network of a container that still exists")
	}
	recovered, err := after.RecoverContainerNet(candidate.ContainerID, deploymentID, candidateSpec.InboundAddr, candidateSpec.OutboundAddr)
	if err != nil {
		t.Fatalf("recovering the other placement after the sweep: %v", err)
	}
	if recovered.HostVeth != candidate.HostVeth {
		t.Fatalf("recovered veth = %s, want %s", recovered.HostVeth, candidate.HostVeth)
	}

	// Nothing names the candidate after another restart, so it is stale.
	restarted := New(prefix, 0)
	if _, err := restarted.RecoverContainerNet(serving.ContainerID, deploymentID, servingSpec.InboundAddr, servingSpec.OutboundAddr); err != nil {
		t.Fatal(err)
	}
	restarted.CleanupContainerNets(deploymentID, nil)
	if exists(candidate) {
		t.Fatal("a network no container uses survived the sweep")
	}
	if !exists(serving) {
		t.Fatal("the sweep removed a network this process tracks")
	}
}
