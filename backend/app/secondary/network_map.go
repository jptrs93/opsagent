package secondary

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"slices"
	"strings"
	"sync"

	"github.com/jptrs93/opsagent/backend/apigen"
	"github.com/jptrs93/opsagent/backend/lib/network"
	"github.com/jptrs93/opsagent/backend/lib/wgkey"
	"github.com/jptrs93/opsagent/backend/storage"
	"github.com/jptrs93/opsagent/backend/storage/secondarydb/state"
)

var (
	ErrStaleClusterNetMap = errors.New("stale cluster network map")
	clusterNetMapMu       sync.Mutex
)

// acceptClusterNetMap validates and persists a received map. Acceptance is
// session-based: the first map accepted in a session replaces whatever is
// cached unconditionally — the primary is the sole author of the map, so its
// snapshot at connect is authoritative even when a restore rolled its history
// (and stamps) backwards. Within a session the stream is ordered and stamps
// only grow, so a lower stamp can only be a coalescing artifact and is
// rejected as stale.
func acceptClusterNetMap(ctx context.Context, store *state.Service, candidate *apigen.ClusterNetMap, nodeID int32, expectedPrefix network.Prefix, sessionSnapshot bool) (*apigen.NetMapStatus, error) {
	next, prefix, err := validateClusterNetMap(candidate, nodeID, expectedPrefix)
	if err != nil {
		return nil, err
	}

	clusterNetMapMu.Lock()
	defer clusterNetMapMu.Unlock()
	if !sessionSnapshot {
		current, _, cached, err := cachedClusterNetMap(ctx, store, nodeID, expectedPrefix)
		if err != nil {
			return nil, err
		}
		if cached {
			if next.DerivedFromSeq < current.DerivedFromSeq {
				return nil, fmt.Errorf("%w: got seq %d, persisted %d", ErrStaleClusterNetMap, next.DerivedFromSeq, current.DerivedFromSeq)
			}
			if next.DerivedFromSeq == current.DerivedFromSeq {
				return statusForClusterNetMap(current, ""), nil
			}
		}
	}

	store.MustSetLocalKV(storage.LocalKVWorkerClusterNetMap, next.Encode())
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
func cachedClusterNetMap(ctx context.Context, store *state.Service, nodeID int32, expectedPrefix network.Prefix) (*apigen.ClusterNetMap, network.Prefix, bool, error) {
	encoded, ok := store.FetchLocalKV(storage.LocalKVWorkerClusterNetMap)
	if !ok {
		return nil, network.Prefix{}, false, nil
	}
	wire, err := apigen.DecodeClusterNetMap(encoded)
	if err != nil {
		return discardCachedClusterNetMap(ctx, store, fmt.Errorf("decoding cached cluster network map: %w", err))
	}
	normalized, prefix, err := validateClusterNetMap(wire, nodeID, expectedPrefix)
	if err != nil {
		return discardCachedClusterNetMap(ctx, store, fmt.Errorf("validating cached cluster network map: %w", err))
	}
	return normalized, prefix, true, nil
}

// discardCachedClusterNetMap drops an unusable cached map and reports it as
// absent. Only a store failure is an error: the content is already known to be
// worthless, so failing to delete it costs nothing but a repeat of this warning.
func discardCachedClusterNetMap(ctx context.Context, store *state.Service, cause error) (*apigen.ClusterNetMap, network.Prefix, bool, error) {
	slog.WarnContext(ctx, "discarding unusable cached cluster network map; waiting for the primary to republish", "err", cause)
	if err := store.DeleteLocalKV(storage.LocalKVWorkerClusterNetMap); err != nil {
		return nil, network.Prefix{}, false, fmt.Errorf("discarding unusable cached cluster network map: %w", err)
	}
	return nil, network.Prefix{}, false, nil
}

func cachedClusterNetMapStatus(ctx context.Context, store *state.Service, nodeID int32, expectedPrefix network.Prefix, reconcileErr string) (*apigen.NetMapStatus, error) {
	current, _, ok, err := cachedClusterNetMap(ctx, store, nodeID, expectedPrefix)
	if err != nil || !ok {
		return nil, err
	}
	return statusForClusterNetMap(current, reconcileErr), nil
}

func statusForClusterNetMap(current *apigen.ClusterNetMap, reconcileErr string) *apigen.NetMapStatus {
	return &apigen.NetMapStatus{
		PersistedSeq:        current.DerivedFromSeq,
		ReconciliationError: reconcileErr,
	}
}

func validateClusterNetMap(candidate *apigen.ClusterNetMap, nodeID int32, expectedPrefix network.Prefix) (*apigen.ClusterNetMap, network.Prefix, error) {
	if candidate == nil {
		return nil, network.Prefix{}, fmt.Errorf("cluster network map is nil")
	}
	if candidate.DerivedFromSeq < 0 {
		return nil, network.Prefix{}, fmt.Errorf("cluster network map derived_from_seq is negative")
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
		DerivedFromSeq: candidate.DerivedFromSeq,
		TargetNodeID:   candidate.TargetNodeID,
		UlaPrefix:      slices.Clone(candidate.UlaPrefix),
		Nodes:          make([]*apigen.ClusterNetMapNode, 0, len(candidate.Nodes)),
		Routes:         make([]*apigen.ClusterNetMapRoute, 0, len(candidate.Routes)),
		PolicyRules:    make([]*apigen.NetPolicyRule, 0, len(candidate.PolicyRules)),
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
		wgPublicKey, err := wgkey.ValidatePublic(node.WgPublicKey)
		if err != nil {
			return nil, network.Prefix{}, fmt.Errorf("node %d has invalid WireGuard public key: %w", node.NodeID, err)
		}
		wgListenPort := node.WgListenPort
		if wgPublicKey == "" {
			wgListenPort = 0
		} else if wgListenPort < 1 || wgListenPort > 65535 {
			return nil, network.Prefix{}, fmt.Errorf("node %d has WireGuard key but invalid listen port %d", node.NodeID, node.WgListenPort)
		}
		normalized.Nodes = append(normalized.Nodes, &apigen.ClusterNetMapNode{NodeID: node.NodeID, UnderlayAddress: underlay, WgPublicKey: wgPublicKey, WgListenPort: wgListenPort})
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

	for _, rule := range candidate.PolicyRules {
		if err := network.ValidateNetMapPolicyRule(rule); err != nil {
			return nil, network.Prefix{}, fmt.Errorf("cluster network map policy rule: %w", err)
		}
		normalized.PolicyRules = append(normalized.PolicyRules, &apigen.NetPolicyRule{
			Source:      &apigen.NetPolicyPeer{SpaceID: rule.Source.SpaceID, DeploymentID: rule.Source.DeploymentID},
			Destination: &apigen.NetPolicyPeer{SpaceID: rule.Destination.SpaceID, DeploymentID: rule.Destination.DeploymentID},
			Ports:       slices.Clone(rule.Ports),
		})
	}
	return normalized, prefix, nil
}
