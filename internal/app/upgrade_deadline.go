package app

import "github.com/GhostFlying/tend/internal/store"

// Zero retains historical batch reads; new batches always persist their cutoff.
func capUpgradeDeadline(r *store.UpgradeRow, deadline int64) int64 {
	if r.View.OverallDeadlineMillis > 0 {
		return min(deadline, r.View.OverallDeadlineMillis)
	}
	return deadline
}
func upgradeBatchExpired(r store.UpgradeRow, now int64) bool {
	return r.View.OverallDeadlineMillis > 0 && now >= r.View.OverallDeadlineMillis
}
