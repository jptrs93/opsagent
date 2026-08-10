// Package primarydb implements the primary node's persistent storage: the
// full control plane — users, auth, secrets, configs, assets, nodes,
// enrollment, spaces, deployment configs — plus the cluster-wide scheduled
// instance and status records. The worker-side storage lives in the fully
// independent secondarydb package; the shared in-memory runtime view both
// build on is storage/instancecache.
//
// Storage failures indicate unrecoverable local state. Outside auth lookups,
// where not-found is an expected result, callers use Must methods and rely on
// the process supervisor to restart OpenDeploy and rebuild in-memory state.
package primarydb
