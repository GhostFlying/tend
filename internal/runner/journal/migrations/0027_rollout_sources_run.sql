-- Per-event dirty marking must not scan retained historical source metadata.
CREATE INDEX rollout_sources_run ON rollout_sources(run_id);
