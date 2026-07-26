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

	mu              sync.Mutex
	refreshMu       sync.Mutex
	current         *apigen.ClusterNetMap
	subscribers     map[*subscriber]struct{}
	nodeUpdates     <-chan apigen.ClusterNode
	instanceUpdates <-chan apigen.ScheduledInstanceState
	unsubscribe     []func()
	closeOnce       sync.Once

	// Acknowledgement state is kept under its own lock so recording a worker's
	// applied sequence never contends with rendering or publishing a map.
	ackMu      sync.Mutex
	applied    map[int32]int64
	ackUpdates chan struct{}
}

func New(store *sqlite.PrimaryStorage, prefix network.Prefix) (*Publisher, error) {
	if store == nil {
		return nil, fmt.Errorf("network-map store is nil")
	}
	if prefix.IsZero() {
		return nil, fmt.Errorf("network-map prefix is not configured")
	}
	nodeSub, unsubscribeNodes := store.SubscribeNodeUpdates()
	_, instanceUpdates, unsubscribeInstances := store.MustFetchScheduledSnapshotAndSubscribe(nil)
	p := &Publisher{
		store:           store,
		prefix:          prefix,
		subscribers:     make(map[*subscriber]struct{}),
		nodeUpdates:     nodeSub.Ch,
		instanceUpdates: instanceUpdates,
		unsubscribe:     []func(){unsubscribeNodes, unsubscribeInstances},
		applied:         make(map[int32]int64),
		ackUpdates:      make(chan struct{}, 1),
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
		case _, ok := <-p.instanceUpdates:
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
	nodes, instances := p.store.FetchNetworkMapInputs()
	next, err := render(p.prefix, nodes, instances)
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

func render(prefix network.Prefix, nodes []*sqlite.Node, instances []apigen.ScheduledInstanceState) (*apigen.ClusterNetMap, error) {
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

	// Routes are a pure function of assignments. Nothing here reads runner status:
	// a placement's route follows its target state, so a container restart, a
	// crash, or a status message arriving out of order cannot move a route or
	// churn the map's sequence.
	//
	// Every live placement owns the /120 covering its runs, for its whole life.
	// The serving placement's node additionally owns the /100 covering the whole
	// instance, which is what carries the stable inbound address. Because a
	// draining placement keeps its own /120, flipping the /100 to a replacement
	// never strands replies to work still in flight on the old one.
	routesByPrefix := make(map[netip.Prefix]int32, len(instances)+len(instances)/2)
	setRoute := func(destination netip.Prefix, nodeID int32) error {
		if existing, ok := routesByPrefix[destination]; ok && existing != nodeID {
			return fmt.Errorf("prefix %s is claimed by nodes %d and %d", destination, existing, nodeID)
		}
		routesByPrefix[destination] = nodeID
		return nil
	}
	for _, item := range instances {
		cfg := item.Config
		inst := item.Instance
		if inst.ID <= 0 || cfg.ID <= 0 || !inst.State.WantsRunning() ||
			cfg.Spec.Networking.Mode != apigen.NetworkingMode_NETWORKING_MODE_VIRTUAL {
			continue
		}
		if _, ok := knownNodes[inst.NodeID]; !ok {
			return nil, fmt.Errorf("scheduled instance %d references unknown node %d", inst.ID, inst.NodeID)
		}
		placement, err := prefix.PlacementCIDR(cfg.Identity.SpaceID, cfg.ID, inst.InstanceOrdinal, inst.ID)
		if err != nil {
			return nil, fmt.Errorf("deriving placement prefix for scheduled instance %d: %w", inst.ID, err)
		}
		if err := setRoute(placement, inst.NodeID); err != nil {
			return nil, err
		}
		if inst.State != apigen.ScheduledInstanceTarget_SCHEDULED_INSTANCE_TARGET_RUN_SERVING {
			continue
		}
		instancePrefix, err := prefix.InstanceCIDR(cfg.Identity.SpaceID, cfg.ID, inst.InstanceOrdinal)
		if err != nil {
			return nil, fmt.Errorf("deriving instance prefix for scheduled instance %d: %w", inst.ID, err)
		}
		// Two serving placements of one ordinal is a scheduler bug, not a
		// transient: the map has no way to express it and must not guess.
		if err := setRoute(instancePrefix, inst.NodeID); err != nil {
			return nil, fmt.Errorf("deployment %d ordinal %d has more than one serving placement: %w",
				cfg.ID, inst.InstanceOrdinal, err)
		}
	}
	routes := make([]*apigen.ClusterNetMapRoute, 0, len(routesByPrefix))
	for destination, nodeID := range routesByPrefix {
		routes = append(routes, &apigen.ClusterNetMapRoute{LogicalPrefix: destination.String(), HostingNodeID: nodeID})
	}
	slices.SortFunc(routes, func(a, b *apigen.ClusterNetMapRoute) int {
		if c := strings.Compare(a.LogicalPrefix, b.LogicalPrefix); c != 0 {
			return c
		}
		return cmp.Compare(a.HostingNodeID, b.HostingNodeID)
	})
	return &apigen.ClusterNetMap{UlaPrefix: prefix.Bytes(), Nodes: netNodes, Routes: routes}, nil
}
