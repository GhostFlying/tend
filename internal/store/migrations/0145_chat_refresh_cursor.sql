-- Prior replaced notices have no projection metadata. Retain the existing
-- expiry boundary for old cursors and track subsequent Chat-visible gaps.
ALTER TABLE refresh_stream_state ADD COLUMN chat_expired_through INTEGER NOT NULL DEFAULT 0
 CHECK(chat_expired_through>=0 AND chat_expired_through<=expired_through);
UPDATE refresh_stream_state SET chat_expired_through=expired_through;
