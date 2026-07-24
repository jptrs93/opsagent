// Package netmappublisher renders, persists, and distributes complete cluster
// network maps. Published snapshots are immutable.
package netmappublisher

import (
	"cmp"
	"context"
	"fmt"
	"log/slog"
	"math"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/google/uuid"
	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

type subscriber struct {
	nodeID  int32
	updates chan *apigen.ClusterNetMap
}

type Publisher struct {
	store  *sqlite.PrimaryStorage
	prefix network.Prefix

	mu          sync.Mutex
	refreshMu   sync.Mutex
	current     *apigen.ClusterNetMap
	subscribers map[*subscriber]struct{}
	nodeUpdates <-chan apigen.ClusterNode
	depUpdates  <-chan apigen.DeploymentWithStatus
	unsubscribe []func()
	closeOnce   sync.Once
}

func New(store *sqlite.PrimaryStorage, prefix network.Prefix) (*Publisher, error) {
	if store == nil {
		return nil, fmt.Errorf("network-map store is nil")
	}
	if prefix.IsZero() {
		return nil, fmt.Errorf("network-map prefix is not configured")
	}
	nodeSub, unsubscribeNodes := store.SubscribeNodeUpdates()
	_, deploymentUpdates, unsubscribeDeployments := store.MustFetchSnapshotAndSubscribe(nil)
	p := &Publisher{
		store:       store,
		prefix:      prefix,
		subscribers: make(map[*subscriber]struct{}),
		nodeUpdates: nodeSub.Ch,
		depUpdates:  deploymentUpdates,
		unsubscribe: []func(){unsubscribeNodes, unsubscribeDeployments},
	}
	if persisted, ok := store.FetchLocalKV(sqlite.LocalKVPrimaryClusterNetMap); ok {
		current, err := apigen.DecodeClusterNetMap(persisted)
		if err != nil {
			p.Close()
			return nil, fmt.Errorf("decoding persisted cluster network map: %w", err)
		}
		if current.Generation == "" || current.Sequence <= 0 || current.TargetNodeID != 0 {
			p.Close()
			return nil, fmt.Errorf("persisted cluster network map has invalid publication metadata")
		}
		p.current = current
	}
	if err := p.Refresh(); err != nil {
		p.Close()
		return nil, err
	}
	return p, nil
}

func (p *Publisher) Close() {
	p.closeOnce.Do(func() {
		for _, unsubscribe := range p.unsubscribe {
			unsubscribe()
		}
	})
}

func (p *Publisher) Run(ctx context.Context) {
	defer p.Close()
	for {
		select {
		case <-ctx.Done():
			return
		case _, ok := <-p.nodeUpdates:
			if !ok {
				return
			}
		case _, ok := <-p.depUpdates:
			if !ok {
				return
			}
		}
		if err := p.Refresh(); err != nil {
			slog.Error("refreshing cluster network map failed", "err", err)
		}
	}
}

// Refresh synchronously renders and durably stores changed topology. It is used
// by enrollment to avoid serving a snapshot from before the node transaction.
func (p *Publisher) Refresh() error {
	p.refreshMu.Lock()
	defer p.refreshMu.Unlock()
	nodes, deployments := p.store.FetchNetworkMapInputs()
	next, err := render(p.prefix, nodes, deployments)
	if err != nil {
		return err
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.current != nil && slices.Equal(canonicalContent(p.current), canonicalContent(next)) {
		return nil
	}
	if p.current == nil {
		next.Generation = uuid.NewString()
		next.Sequence = 1
	} else {
		if p.current.Sequence == math.MaxInt64 {
			return fmt.Errorf("cluster network map sequence exhausted")
		}
		next.Generation = p.current.Generation
		next.Sequence = p.current.Sequence + 1
	}
	p.store.MustSetLocalKV(sqlite.LocalKVPrimaryClusterNetMap, next.Encode())
	p.current = next
	for sub := range p.subscribers {
		publishLatest(sub.updates, mapForNode(next, sub.nodeID))
	}
	return nil
}

func (p *Publisher) SnapshotForNode(nodeID int32) *apigen.ClusterNetMap {
	p.mu.Lock()
	defer p.mu.Unlock()
	return mapForNode(p.current, nodeID)
}

// SnapshotAndSubscribe atomically returns the latest targeted map and a
// capacity-one stream which replaces queued obsolete maps.
func (p *Publisher) SnapshotAndSubscribe(nodeID int32) (*apigen.ClusterNetMap, <-chan *apigen.ClusterNetMap, func()) {
	p.mu.Lock()
	defer p.mu.Unlock()
	sub := &subscriber{nodeID: nodeID, updates: make(chan *apigen.ClusterNetMap, 1)}
	p.subscribers[sub] = struct{}{}
	var once sync.Once
	return mapForNode(p.current, nodeID), sub.updates, func() {
		once.Do(func() {
			p.mu.Lock()
			delete(p.subscribers, sub)
			p.mu.Unlock()
		})
	}
}

func publishLatest(ch chan *apigen.ClusterNetMap, next *apigen.ClusterNetMap) {
	select {
	case ch <- next:
	default:
		select {
		case <-ch:
		default:
		}
		select {
		case ch <- next:
		default:
		}
	}
}

func mapForNode(source *apigen.ClusterNetMap, nodeID int32) *apigen.ClusterNetMap {
	if source == nil {
		return nil
	}
	next := *source
	next.TargetNodeID = nodeID
	next.UlaPrefix = slices.Clone(source.UlaPrefix)
	next.Nodes = slices.Clone(source.Nodes)
	next.Routes = slices.Clone(source.Routes)
	return &next
}

func canonicalContent(source *apigen.ClusterNetMap) []byte {
	canonical := mapForNode(source, 0)
	canonical.Generation = ""
	canonical.Sequence = 0
	return canonical.Encode()
}

func render(prefix network.Prefix, nodes []*sqlite.Node, deployments []apigen.DeploymentWithStatus) (*apigen.ClusterNetMap, error) {
	netNodes := make([]*apigen.ClusterNetMapNode, 0, len(nodes))
	knownNodes := make(map[int32]struct{}, len(nodes))
	underlayBits := 0
	for _, node := range nodes {
		if node == nil || node.ID <= 0 {
			continue
		}
		underlay := ""
		if len(node.Addresses) > 0 {
			underlay = strings.TrimSpace(node.Addresses[0])
			if underlay != "" {
				addr, err := netip.ParseAddr(underlay)
				if err != nil || addr.Zone() != "" {
					return nil, fmt.Errorf("node %d has invalid underlay address %q", node.ID, underlay)
				}
				addr = addr.Unmap()
				if underlayBits != 0 && addr.BitLen() != underlayBits {
					return nil, fmt.Errorf("node %d underlay address family differs from cluster", node.ID)
				}
				underlayBits = addr.BitLen()
				underlay = addr.String()
			}
		}
		knownNodes[node.ID] = struct{}{}
		netNodes = append(netNodes, &apigen.ClusterNetMapNode{NodeID: node.ID, UnderlayAddress: underlay})
	}
	slices.SortFunc(netNodes, func(a, b *apigen.ClusterNetMapNode) int { return cmp.Compare(a.NodeID, b.NodeID) })

	routes := make([]*apigen.ClusterNetMapRoute, 0, len(deployments)*2)
	for _, item := range deployments {
		cfg := item.Config
		runner := item.Status.Runner
		if cfg.ID <= 0 || runner.DeploymentConfigVersion <= 0 ||
			(runner.Status != apigen.RunningStatus_STARTING && runner.Status != apigen.RunningStatus_RUNNING) ||
			len(runner.Endpoints) == 0 {
			continue
		}
		for _, endpoint := range runner.Endpoints {
			if endpoint == nil || endpoint.NodeID <= 0 {
				return nil, fmt.Errorf("deployment %d has invalid observed endpoint", cfg.ID)
			}
			if _, ok := knownNodes[endpoint.NodeID]; !ok {
				return nil, fmt.Errorf("deployment %d endpoint references unknown node %d", cfg.ID, endpoint.NodeID)
			}
			inbound, err := netip.ParseAddr(endpoint.Address)
			if err != nil {
				return nil, fmt.Errorf("parsing deployment %d inbound address: %w", cfg.ID, err)
			}
			identity, err := prefix.ParseAddr(inbound)
			if err != nil || !identity.IsInbound() || identity.DeploymentID != cfg.ID || identity.Ordinal != endpoint.Ordinal {
				return nil, fmt.Errorf("deployment %d has invalid observed inbound address %q", cfg.ID, endpoint.Address)
			}
			outbound, err := prefix.OutboundAddr(identity.SpaceID, cfg.ID, identity.Ordinal, runner.DeploymentConfigVersion, runner.NumberOfRestarts+1)
			if err != nil {
				return nil, fmt.Errorf("deriving deployment %d outbound address: %w", cfg.ID, err)
			}
			routes = append(routes,
				&apigen.ClusterNetMapRoute{LogicalAddress: inbound.String(), HostingNodeID: endpoint.NodeID},
				&apigen.ClusterNetMapRoute{LogicalAddress: outbound.String(), HostingNodeID: endpoint.NodeID},
			)
		}
	}
	slices.SortFunc(routes, func(a, b *apigen.ClusterNetMapRoute) int {
		if c := strings.Compare(a.LogicalAddress, b.LogicalAddress); c != 0 {
			return c
		}
		return cmp.Compare(a.HostingNodeID, b.HostingNodeID)
	})
	return &apigen.ClusterNetMap{UlaPrefix: prefix.Bytes(), Nodes: netNodes, Routes: routes}, nil
}
