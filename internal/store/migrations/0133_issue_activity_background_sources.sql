-- Issue activity starts from the Issue's batches, never the global stream of
-- task completions. Event diagnostics can also look up an individual source.
CREATE INDEX background_batches_issue_sources ON background_batches(work_item_id,batch_id);
CREATE INDEX background_sources_event_batch ON background_sources(event_id,batch_id);
