-- Index reverse reply attribution independently of the direct reply index.
CREATE INDEX conversation_reply_sources_source ON conversation_reply_sources(source_id,reply_id);
