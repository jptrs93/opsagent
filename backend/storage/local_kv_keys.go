package storage

// Keys into a worker's machine-local local_kv table in secondarydb. The keys
// live here because primary and worker code exchange the state stored under
// them; the primary itself no longer has a local_kv table.
const (
	// LocalKVClusterNetwork caches the cluster network parameters on a worker.
	LocalKVClusterNetwork = "cluster_network"
	// LocalKVWorkerClusterNetMap stores the worker's last accepted full map.
	LocalKVWorkerClusterNetMap = "worker_cluster_net_map"
	// LocalKVWorkerRetiredNetMapGenerations is retired along with map
	// generations. The key is deleted at worker startup.
	LocalKVWorkerRetiredNetMapGenerations = "worker_retired_net_map_generations"
	// LocalKVAcmeState stores the worker's last received ACME cert bindings and
	// pending HTTP-01 challenges.
	LocalKVAcmeState = "acme_state"
)
