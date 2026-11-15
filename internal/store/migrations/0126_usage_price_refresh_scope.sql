-- D245: catalog refreshes retain freshness but invalidate only native models
-- whose selected quote changed. App computes exact/case/slug semantics in the
-- same transaction; existing dirty facts and all immutable samples remain.
DROP TRIGGER usage_projection_prices_insert;
DROP TRIGGER usage_projection_prices_update;
CREATE INDEX token_usage_context_model ON token_usage_contexts(model);
CREATE INDEX token_usage_fact_context ON token_usage_facts(context_id);
