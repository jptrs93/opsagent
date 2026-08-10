package storage

// Keys into each node's machine-local local_kv table. The table exists in both
// primarydb and secondarydb; the keys live here because primary and worker
// code exchange the state stored under them.
const (
	// LocalKVClusterNetwork caches the legacy cluster network parameters on a
	// worker during rolling upgrades.
	LocalKVClusterNetwork = "cluster_network"
	// LocalKVPrimaryClusterNetMap stores the primary's latest target-neutral
	// publication, including generation and sequence.
	LocalKVPrimaryClusterNetMap = "primary_cluster_net_map"
	// LocalKVWorkerClusterNetMap stores the worker's last accepted full map.
	LocalKVWorkerClusterNetMap = "worker_cluster_net_map"
	// LocalKVWorkerRetiredNetMapGenerations prevents returning to a superseded
	// control-plane history after accepting a new generation.
	LocalKVWorkerRetiredNetMapGenerations = "worker_retired_net_map_generations"
)
