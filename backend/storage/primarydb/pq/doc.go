// Package pq is the primary database's SQL layer. Every query is a method on
// *Queries, and nothing outside this package holds a database handle. The
// state.Service above it owns the write lock, transactions, update triggers,
// and publication, and delegates all SQL here; Tx runs a function against a
// transaction-bound *Queries.
//
// sql/ holds the schema and migrations.
package pq
