-- Correlated overlap checks start from native identity, then resolve the
-- original current fact without repeatedly scanning the entire current ledger.
CREATE INDEX token_usage_legacy_candidates_native
 ON token_usage_legacy_candidates(native_source,native_request_id,legacy_seq);
CREATE INDEX token_usage_current_fact
 ON token_usage_current(fact_seq,conflict);
