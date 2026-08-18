package storage

// Keys into each node's machine-local local_kv table. The table exists in both
// primarydb and secondarydb; the keys live here because primary and worker
// code exchange the state stored under them.
const (
	// LocalKVClusterNetwork caches the cluster network parameters on a worker.
	LocalKVClusterNetwork = "cluster_network"
	// LocalKVPrimaryClusterNetMap is retired: the primary's map is derived state
	// and no longer persisted. The key is deleted at publisher startup.
	LocalKVPrimaryClusterNetMap = "primary_cluster_net_map"
	// LocalKVWorkerClusterNetMap stores the worker's last accepted full map.
	LocalKVWorkerClusterNetMap = "worker_cluster_net_map"
	// LocalKVWorkerRetiredNetMapGenerations is retired along with map
	// generations. The key is deleted at worker startup.
	LocalKVWorkerRetiredNetMapGenerations = "worker_retired_net_map_generations"
	// LocalKVAcmeState stores the worker's last received ACME cert bindings and
	// pending HTTP-01 challenges.
	LocalKVAcmeState = "acme_state"
)
