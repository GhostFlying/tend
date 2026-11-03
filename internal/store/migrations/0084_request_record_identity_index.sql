-- Request identities occur in effect reconciliation without a known domain.
-- Preserve the original EXISTS semantics across every domain and status.
CREATE INDEX request_records_identity_status
 ON request_records(request_id,status);
