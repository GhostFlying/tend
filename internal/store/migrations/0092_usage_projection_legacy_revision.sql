-- A legacy revision can retire the sequence that quarantined a native peer.
DROP TRIGGER usage_projection_current_update;
CREATE TRIGGER usage_projection_current_update AFTER UPDATE ON token_usage_current
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty VALUES(NEW.source,NEW.request_id);
 INSERT OR IGNORE INTO token_usage_dirty SELECT * FROM token_usage_overlap_identities WHERE EXISTS(SELECT 1 FROM token_usage_legacy_candidates WHERE legacy_seq IN (OLD.fact_seq,NEW.fact_seq) OR (native_source=NEW.source AND native_request_id=NEW.request_id)) OR EXISTS(SELECT 1 FROM token_usage_legacy_links WHERE legacy_seq IN (OLD.fact_seq,NEW.fact_seq) OR (native_source=NEW.source AND native_request_id=NEW.request_id));
END;
