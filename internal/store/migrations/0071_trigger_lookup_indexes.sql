-- Resolve source provenance without scanning every retained background source.
CREATE INDEX background_sources_message_seq ON background_sources(message_id,seq DESC);
CREATE INDEX trigger_events_created ON trigger_events(created_at DESC,id DESC);
