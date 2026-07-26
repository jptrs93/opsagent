package secondary

import (
	"bytes"
	"cmp"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
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
	current, _, cached, err := cachedClusterNetMap(store, nodeID, expectedPrefix)
	if err != nil {
		return nil, err
	}
	if cached {
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

// cachedClusterNetMap returns the worker's persisted map, or reports that it has
// none.
//
// A cached map this build cannot read is discarded rather than raised. The cache
// is an optimisation — the primary serves a complete map on every reconnect — but
// it outlives the binary that wrote it, so a release that changes what a map may
// contain inherits whatever the previous release persisted. Treating that as an
// error is unrecoverable in both directions: it panics the worker before it can
// connect, and it makes acceptClusterNetMap reject the very map that would
// replace it.
//
// The discard deliberately does not retire the stale generation. Generations are
// persisted by the primary and survive its restarts, so the map that supersedes
// this one almost always carries the same generation; retiring it here would
// refuse every future map from that primary.
func cachedClusterNetMap(store *sqlite.SecondaryStorage, nodeID int32, expectedPrefix network.Prefix) (*apigen.ClusterNetMap, network.Prefix, bool, error) {
	encoded, ok := store.FetchLocalKV(sqlite.LocalKVWorkerClusterNetMap)
	if !ok {
		return nil, network.Prefix{}, false, nil
	}
	wire, err := apigen.DecodeClusterNetMap(encoded)
	if err != nil {
		return discardCachedClusterNetMap(store, fmt.Errorf("decoding cached cluster network map: %w", err))
	}
	normalized, prefix, err := validateClusterNetMap(wire, nodeID, expectedPrefix)
	if err != nil {
		return discardCachedClusterNetMap(store, fmt.Errorf("validating cached cluster network map: %w", err))
	}
	return normalized, prefix, true, nil
}

// discardCachedClusterNetMap drops an unusable cached map and reports it as
// absent. Only a store failure is an error: the content is already known to be
// worthless, so failing to delete it costs nothing but a repeat of this warning.
func discardCachedClusterNetMap(store *sqlite.SecondaryStorage, cause error) (*apigen.ClusterNetMap, network.Prefix, bool, error) {
	slog.Warn("discarding unusable cached cluster network map; waiting for the primary to republish", "err", cause)
	if err := store.DeleteLocalKV(sqlite.LocalKVWorkerClusterNetMap); err != nil {
		return nil, network.Prefix{}, false, fmt.Errorf("discarding unusable cached cluster network map: %w", err)
	}
	return nil, network.Prefix{}, false, nil
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

	logicalPrefixes := make(map[netip.Prefix]struct{}, len(candidate.Routes))
	for _, route := range candidate.Routes {
		if route == nil {
			return nil, network.Prefix{}, fmt.Errorf("cluster network map contains nil route")
		}
		if _, ok := knownNodes[route.HostingNodeID]; !ok {
			return nil, network.Prefix{}, fmt.Errorf("route %q references unknown node %d", route.LogicalPrefix, route.HostingNodeID)
		}
		destination, err := netip.ParsePrefix(strings.TrimSpace(route.LogicalPrefix))
		if err != nil || destination.Addr().Zone() != "" {
			return nil, network.Prefix{}, fmt.Errorf("invalid logical route prefix %q", route.LogicalPrefix)
		}
		if err := prefix.ValidateRoutedPrefix(destination); err != nil {
			return nil, network.Prefix{}, err
		}
		if _, exists := logicalPrefixes[destination]; exists {
			return nil, network.Prefix{}, fmt.Errorf("cluster network map contains duplicate route %s", destination)
		}
		logicalPrefixes[destination] = struct{}{}
		normalized.Routes = append(normalized.Routes, &apigen.ClusterNetMapRoute{LogicalPrefix: destination.String(), HostingNodeID: route.HostingNodeID})
	}
	slices.SortFunc(normalized.Routes, func(a, b *apigen.ClusterNetMapRoute) int {
		if c := strings.Compare(a.LogicalPrefix, b.LogicalPrefix); c != 0 {
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
