// Package pq is the primary database's SQL layer. Every query — the
// sqlc-generated ones and the hand-written ones in this package — is a method
// on *Queries, and nothing outside this package holds a database handle. The
// primarydb.Storage above it owns locking, caches, invariants, and
// notification, and delegates all SQL here; Tx runs a function against a
// transaction-bound *Queries.
//
// sql/ holds the schema, sqlc queries, and migrations; regenerate with sqlc
// after editing sql/queries.sql.
package pq
