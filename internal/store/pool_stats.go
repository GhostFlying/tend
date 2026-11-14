package store

import "database/sql"

// PoolStats is a point-in-time connection pool diagnostic, not a transaction
// snapshot or a count of SQLite locks. Counters belong to this Store lifetime.
type PoolStats struct {
	Reader sql.DBStats
	Writer sql.DBStats
}

// PoolStats returns bounded connection counts and cumulative wait/close counters
// without opening a database connection or reading ledger content.
func (s *Store) PoolStats() PoolStats {
	return PoolStats{Reader: s.read.Stats(), Writer: s.write.Stats()}
}
