// Package sqlite implements OpenDeploy's persistent storage adapters.
//
// Storage failures indicate unrecoverable local state. Outside auth lookups,
// where not-found is an expected result, callers use Must methods and rely on
// the process supervisor to restart OpenDeploy and rebuild in-memory state.
package sqlite
