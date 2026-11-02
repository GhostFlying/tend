package store

// UsagePendingOverlapSQLFor requires a compile-time SQL fact alias. A native
// request cannot count on top of unresolved, still-current legacy evidence.
func UsagePendingOverlapSQLFor(alias string) string {
	return `EXISTS(SELECT 1 FROM token_usage_legacy_candidates candidate
 JOIN token_usage_current old_current ON old_current.fact_seq=candidate.legacy_seq
 WHERE old_current.conflict=0 AND candidate.native_source=` + alias + `.source AND candidate.native_request_id=` + alias + `.request_id
 AND NOT EXISTS(SELECT 1 FROM token_usage_legacy_candidates resolved
 JOIN token_usage_current native_current ON native_current.source=resolved.native_source AND native_current.request_id=resolved.native_request_id
 WHERE resolved.legacy_seq=candidate.legacy_seq AND resolved.matched=1 AND native_current.conflict=0))`
}

// UsageLegacyLinkedSQL requires a compile-time legacy-sequence expression.
// If the canonical request becomes conflicted, the old limited measurement
// becomes effective again instead of disappearing from the ledger's totals.
func UsageLegacyLinkedSQL(sequence string) string {
	return `EXISTS(SELECT 1 FROM token_usage_legacy_links l
 JOIN token_usage_current linked_current ON linked_current.source=l.native_source AND linked_current.request_id=l.native_request_id
 JOIN token_usage_facts linked_fact ON linked_fact.seq=linked_current.fact_seq
 WHERE l.legacy_seq=` + sequence + ` AND linked_current.conflict=0 AND NOT ` + UsagePendingOverlapSQLFor("linked_fact") + `)`
}
