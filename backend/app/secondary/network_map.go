package secondary

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/storage/sqlite"
)

var (
	ErrStaleClusterNetMap       = errors.New("stale cluster network map")
	ErrConflictingClusterNetMap = errors.New("conflicting cluster network map")
	clusterNetMapMu             sync.Mutex
)

func acceptClusterNetMap(store *sqlite.SecondaryStorage, candidate *apigen.ClusterNetMap, nodeID int32, expectedPrefix network.Prefix) (*apigen.NetMapStatus, error) {
	next, prefix, err := validateClusterNetMap(candidate, nodeID, expectedPrefix)
	if err != nil {
		return nil, err
	}

	clusterNetMapMu.Lock()
	defer clusterNetMapMu.Unlock()
	retired, err := loadRetiredNetMapGenerations(store)
	if err != nil {
		return nil, err
	}
	if _, wasRetired := retired[next.Generation]; wasRetired {
		return nil, fmt.Errorf("%w: generation %s was superseded", ErrStaleClusterNetMap, next.Generation)
	}
	if encoded, ok := store.FetchLocalKV(sqlite.LocalKVWorkerClusterNetMap); ok {
		currentWire, err := apigen.DecodeClusterNetMap(encoded)
		if err != nil {
			return nil, fmt.Errorf("decoding cached cluster network map: %w", err)
		}
		current, _, err := validateClusterNetMap(currentWire, nodeID, expectedPrefix)
		if err != nil {
			return nil, fmt.Errorf("validating cached cluster network map: %w", err)
		}
		if next.Generation == current.Generation {
			switch {
			case next.Sequence < current.Sequence:
				return nil, fmt.Errorf("%w: got sequence %d, persisted %d", ErrStaleClusterNetMap, next.Sequence, current.Sequence)
			case next.Sequence == current.Sequence && !bytes.Equal(next.Encode(), current.Encode()):
				return nil, fmt.Errorf("%w: generation %s sequence %d", ErrConflictingClusterNetMap, next.Generation, next.Sequence)
			case next.Sequence == current.Sequence:
				return statusForClusterNetMap(current, ""), nil
			}
		} else {
			retired[current.Generation] = struct{}{}
		}
	}

	retiredJSON, err := json.Marshal(sortedGenerationSet(retired))
	if err != nil {
		return nil, fmt.Errorf("encoding retired network map generations: %w", err)
	}
	store.MustSetLocalKVs(map[string][]byte{
		sqlite.LocalKVWorkerClusterNetMap:            next.Encode(),
		sqlite.LocalKVWorkerRetiredNetMapGenerations: retiredJSON,
	})
	network.Default.SetPrefix(prefix)
	return statusForClusterNetMap(next, ""), nil
}

func cachedClusterNetMap(store *sqlite.SecondaryStorage, nodeID int32, expectedPrefix network.Prefix) (*apigen.ClusterNetMap, network.Prefix, bool, error) {
	encoded, ok := store.FetchLocalKV(sqlite.LocalKVWorkerClusterNetMap)
	if !ok {
		return nil, network.Prefix{}, false, nil
	}
	wire, err := apigen.DecodeClusterNetMap(encoded)
	if err != nil {
		return nil, network.Prefix{}, false, fmt.Errorf("decoding cached cluster network map: %w", err)
	}
	normalized, prefix, err := validateClusterNetMap(wire, nodeID, expectedPrefix)
	if err != nil {
		return nil, network.Prefix{}, false, fmt.Errorf("validating cached cluster network map: %w", err)
	}
	return normalized, prefix, true, nil
}

func cachedClusterNetMapStatus(store *sqlite.SecondaryStorage, nodeID int32, expectedPrefix network.Prefix, reconcileErr string) (*apigen.NetMapStatus, error) {
	current, _, ok, err := cachedClusterNetMap(store, nodeID, expectedPrefix)
	if err != nil || !ok {
		return nil, err
	}
	return statusForClusterNetMap(current, reconcileErr), nil
}

func statusForClusterNetMap(current *apigen.ClusterNetMap, reconcileErr string) *apigen.NetMapStatus {
	return &apigen.NetMapStatus{
		AcceptedGeneration:  current.Generation,
		PersistedSequence:   current.Sequence,
		ReconciliationError: reconcileErr,
	}
}

func validateClusterNetMap(candidate *apigen.ClusterNetMap, nodeID int32, expectedPrefix network.Prefix) (*apigen.ClusterNetMap, network.Prefix, error) {
	if candidate == nil {
		return nil, network.Prefix{}, fmt.Errorf("cluster network map is nil")
	}
	if strings.TrimSpace(candidate.Generation) == "" {
		return nil, network.Prefix{}, fmt.Errorf("cluster network map generation is empty")
	}
	if candidate.Generation != strings.TrimSpace(candidate.Generation) {
		return nil, network.Prefix{}, fmt.Errorf("cluster network map generation contains surrounding whitespace")
	}
	if candidate.Sequence <= 0 {
		return nil, network.Prefix{}, fmt.Errorf("cluster network map sequence must be positive")
	}
	if nodeID <= 0 || candidate.TargetNodeID != nodeID {
		return nil, network.Prefix{}, fmt.Errorf("cluster network map target %d does not match local node %d", candidate.TargetNodeID, nodeID)
	}
	prefix, err := network.ParsePrefix(candidate.UlaPrefix)
	if err != nil {
		return nil, network.Prefix{}, err
	}
	if !expectedPrefix.IsZero() && prefix != expectedPrefix {
		return nil, network.Prefix{}, fmt.Errorf("cluster network map prefix %s does not match configured prefix %s", prefix, expectedPrefix)
	}

	normalized := &apigen.ClusterNetMap{
		Generation:   candidate.Generation,
		Sequence:     candidate.Sequence,
		TargetNodeID: candidate.TargetNodeID,
		UlaPrefix:    slices.Clone(candidate.UlaPrefix),
		Nodes:        make([]*apigen.ClusterNetMapNode, 0, len(candidate.Nodes)),
		Routes:       make([]*apigen.ClusterNetMapRoute, 0, len(candidate.Routes)),
	}
	knownNodes := make(map[int32]struct{}, len(candidate.Nodes))
	targetPresent := false
	underlayBits := 0
	for _, node := range candidate.Nodes {
		if node == nil || node.NodeID <= 0 {
			return nil, network.Prefix{}, fmt.Errorf("cluster network map contains invalid node")
		}
		if _, exists := knownNodes[node.NodeID]; exists {
			return nil, network.Prefix{}, fmt.Errorf("cluster network map contains duplicate node %d", node.NodeID)
		}
		knownNodes[node.NodeID] = struct{}{}
		targetPresent = targetPresent || node.NodeID == nodeID
		underlay := strings.TrimSpace(node.UnderlayAddress)
		if underlay != "" {
			addr, err := netip.ParseAddr(underlay)
			if err != nil || addr.Zone() != "" {
				return nil, network.Prefix{}, fmt.Errorf("node %d has invalid underlay address %q", node.NodeID, underlay)
			}
			addr = addr.Unmap()
			if underlayBits != 0 && addr.BitLen() != underlayBits {
				return nil, network.Prefix{}, fmt.Errorf("node %d underlay address family differs from cluster", node.NodeID)
			}
			underlayBits = addr.BitLen()
			underlay = addr.String()
		}
		normalized.Nodes = append(normalized.Nodes, &apigen.ClusterNetMapNode{NodeID: node.NodeID, UnderlayAddress: underlay})
	}
	if !targetPresent {
		return nil, network.Prefix{}, fmt.Errorf("cluster network map does not contain target node %d", nodeID)
	}
	slices.SortFunc(normalized.Nodes, func(a, b *apigen.ClusterNetMapNode) int { return cmp.Compare(a.NodeID, b.NodeID) })

	logicalAddresses := make(map[netip.Addr]struct{}, len(candidate.Routes))
	for _, route := range candidate.Routes {
		if route == nil {
			return nil, network.Prefix{}, fmt.Errorf("cluster network map contains nil route")
		}
		if _, ok := knownNodes[route.HostingNodeID]; !ok {
			return nil, network.Prefix{}, fmt.Errorf("route %q references unknown node %d", route.LogicalAddress, route.HostingNodeID)
		}
		addr, err := netip.ParseAddr(strings.TrimSpace(route.LogicalAddress))
		if err != nil || addr.Zone() != "" {
			return nil, network.Prefix{}, fmt.Errorf("invalid logical route address %q", route.LogicalAddress)
		}
		if err := prefix.ValidateRoutedAddr(addr); err != nil {
			return nil, network.Prefix{}, err
		}
		if _, exists := logicalAddresses[addr]; exists {
			return nil, network.Prefix{}, fmt.Errorf("cluster network map contains duplicate route %s", addr)
		}
		logicalAddresses[addr] = struct{}{}
		normalized.Routes = append(normalized.Routes, &apigen.ClusterNetMapRoute{LogicalAddress: addr.String(), HostingNodeID: route.HostingNodeID})
	}
	slices.SortFunc(normalized.Routes, func(a, b *apigen.ClusterNetMapRoute) int {
		if c := strings.Compare(a.LogicalAddress, b.LogicalAddress); c != 0 {
			return c
		}
		return cmp.Compare(a.HostingNodeID, b.HostingNodeID)
	})
	return normalized, prefix, nil
}

func loadRetiredNetMapGenerations(store *sqlite.SecondaryStorage) (map[string]struct{}, error) {
	retired := make(map[string]struct{})
	encoded, ok := store.FetchLocalKV(sqlite.LocalKVWorkerRetiredNetMapGenerations)
	if !ok {
		return retired, nil
	}
	var generations []string
	if err := json.Unmarshal(encoded, &generations); err != nil {
		return nil, fmt.Errorf("decoding retired network map generations: %w", err)
	}
	for _, generation := range generations {
		if generation == "" || generation != strings.TrimSpace(generation) {
			return nil, fmt.Errorf("invalid retired network map generation %q", generation)
		}
		retired[generation] = struct{}{}
	}
	return retired, nil
}

func sortedGenerationSet(generations map[string]struct{}) []string {
	out := make([]string, 0, len(generations))
	for generation := range generations {
		out = append(out, generation)
	}
	slices.Sort(out)
	return out
}
