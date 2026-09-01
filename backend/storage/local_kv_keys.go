package storage

// Keys into a secondary's machine-local local_kv table in secondarydb. The keys
// live here because primary and secondary code exchange the state stored under
// them; the primary itself no longer has a local_kv table.
const (
	// LocalKVClusterNetwork caches the cluster network parameters on a secondary.
	LocalKVClusterNetwork = "cluster_network"
	// LocalKVClusterNetMap stores the secondary's last accepted full map.
	LocalKVClusterNetMap = "worker_cluster_net_map"
	// LocalKVAcmeState stores the secondary's last received ACME cert bindings and
	// pending HTTP-01 challenges.
	LocalKVAcmeState = "acme_state"
)
