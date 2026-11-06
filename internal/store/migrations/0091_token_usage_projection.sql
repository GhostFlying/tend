-- Rebuildable projections. Immutable request facts remain the source of truth.
CREATE TABLE token_usage_dirty (
 source TEXT NOT NULL, request_id TEXT NOT NULL,
 PRIMARY KEY(source,request_id)
) WITHOUT ROWID;
CREATE TABLE token_usage_contributions (
 source TEXT NOT NULL, request_id TEXT NOT NULL, fact_seq INTEGER NOT NULL UNIQUE,
 thread_id TEXT NOT NULL, turn_id TEXT NOT NULL, root_turn_id TEXT NOT NULL, run_id TEXT NOT NULL,
 task_id TEXT NOT NULL, conversation_id TEXT NOT NULL,
 dimension_key TEXT NOT NULL, bucket INTEGER NOT NULL, occurred_at INTEGER,
 visible INTEGER NOT NULL, conflict INTEGER NOT NULL, native INTEGER NOT NULL,
 record_json TEXT NOT NULL, dimension_json TEXT NOT NULL, totals_json TEXT NOT NULL,
 PRIMARY KEY(source,request_id)
) WITHOUT ROWID;
CREATE INDEX token_usage_contribution_bucket ON token_usage_contributions(dimension_key,bucket,visible,conflict,occurred_at);
CREATE INDEX token_usage_contribution_turn ON token_usage_contributions(thread_id,turn_id);
CREATE INDEX token_usage_log_turn ON run_logs(json_extract(body_json,'$.thread_id'),json_extract(body_json,'$.turn_id'),run_id) WHERE kind IN ('native.turn_started','native.turn_completed','item.completed');
CREATE INDEX token_usage_actor_conversation ON token_usage_local_sources(conversation_id);
CREATE INDEX token_usage_contribution_run ON token_usage_contributions(run_id);
CREATE INDEX token_usage_contribution_task ON token_usage_contributions(task_id);
CREATE INDEX token_usage_contribution_conversation ON token_usage_contributions(conversation_id);
CREATE TABLE token_usage_summaries (
 dimension_key TEXT NOT NULL, bucket INTEGER NOT NULL,
 dimension_json TEXT NOT NULL, totals_json TEXT NOT NULL,
 conflicts INTEGER NOT NULL, legacy_requests INTEGER NOT NULL,
 first_at INTEGER, last_at INTEGER, max_seq INTEGER NOT NULL,
 PRIMARY KEY(dimension_key,bucket)
) WITHOUT ROWID;
CREATE INDEX token_usage_summary_bucket ON token_usage_summaries(bucket);
INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current;

CREATE VIEW token_usage_overlap_identities AS
 SELECT source,request_id FROM token_usage_current WHERE fact_seq IN
 (SELECT legacy_seq FROM token_usage_legacy_candidates UNION SELECT legacy_seq FROM token_usage_legacy_links)
 UNION SELECT native_source,native_request_id FROM token_usage_legacy_candidates
 UNION SELECT native_source,native_request_id FROM token_usage_legacy_links;

CREATE TRIGGER usage_projection_current_insert AFTER INSERT ON token_usage_current
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty VALUES(NEW.source,NEW.request_id);
 INSERT OR IGNORE INTO token_usage_dirty SELECT * FROM token_usage_overlap_identities WHERE EXISTS(SELECT 1 FROM token_usage_legacy_candidates WHERE legacy_seq=NEW.fact_seq OR (native_source=NEW.source AND native_request_id=NEW.request_id)) OR EXISTS(SELECT 1 FROM token_usage_legacy_links WHERE legacy_seq=NEW.fact_seq OR (native_source=NEW.source AND native_request_id=NEW.request_id));
END;

CREATE TRIGGER usage_projection_binding_insert AFTER INSERT ON token_usage_bindings
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty VALUES(NEW.source,NEW.request_id);
END;

CREATE TRIGGER usage_projection_token_usage_legacy_candidates_insert AFTER INSERT ON token_usage_legacy_candidates
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty
 SELECT * FROM token_usage_overlap_identities;
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=NEW.legacy_seq;
 INSERT OR IGNORE INTO token_usage_dirty VALUES(NEW.native_source,NEW.native_request_id);
END;

CREATE TRIGGER usage_projection_token_usage_legacy_links_insert AFTER INSERT ON token_usage_legacy_links
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty
 SELECT * FROM token_usage_overlap_identities;
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=NEW.legacy_seq;
 INSERT OR IGNORE INTO token_usage_dirty VALUES(NEW.native_source,NEW.native_request_id);
END;

CREATE TRIGGER usage_projection_current_update AFTER UPDATE ON token_usage_current
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty VALUES(NEW.source,NEW.request_id);
 INSERT OR IGNORE INTO token_usage_dirty SELECT * FROM token_usage_overlap_identities WHERE EXISTS(SELECT 1 FROM token_usage_legacy_candidates WHERE legacy_seq=NEW.fact_seq OR (native_source=NEW.source AND native_request_id=NEW.request_id)) OR EXISTS(SELECT 1 FROM token_usage_legacy_links WHERE legacy_seq=NEW.fact_seq OR (native_source=NEW.source AND native_request_id=NEW.request_id));
END;

CREATE TRIGGER usage_projection_binding_update AFTER UPDATE ON token_usage_bindings
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty VALUES(NEW.source,NEW.request_id);
END;

CREATE TRIGGER usage_projection_token_usage_legacy_candidates_update AFTER UPDATE ON token_usage_legacy_candidates
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty
 SELECT * FROM token_usage_overlap_identities;
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=NEW.legacy_seq;
 INSERT OR IGNORE INTO token_usage_dirty VALUES(NEW.native_source,NEW.native_request_id);
END;

CREATE TRIGGER usage_projection_token_usage_legacy_links_update AFTER UPDATE ON token_usage_legacy_links
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty
 SELECT * FROM token_usage_overlap_identities;
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=NEW.legacy_seq;
 INSERT OR IGNORE INTO token_usage_dirty VALUES(NEW.native_source,NEW.native_request_id);
END;

CREATE TRIGGER usage_projection_current_delete AFTER DELETE ON token_usage_current
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty VALUES(OLD.source,OLD.request_id);
 INSERT OR IGNORE INTO token_usage_dirty SELECT * FROM token_usage_overlap_identities WHERE EXISTS(SELECT 1 FROM token_usage_legacy_candidates WHERE legacy_seq=OLD.fact_seq OR (native_source=OLD.source AND native_request_id=OLD.request_id)) OR EXISTS(SELECT 1 FROM token_usage_legacy_links WHERE legacy_seq=OLD.fact_seq OR (native_source=OLD.source AND native_request_id=OLD.request_id));
END;

CREATE TRIGGER usage_projection_binding_delete AFTER DELETE ON token_usage_bindings
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty VALUES(OLD.source,OLD.request_id);
END;

CREATE TRIGGER usage_projection_token_usage_legacy_candidates_delete AFTER DELETE ON token_usage_legacy_candidates
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty
 SELECT * FROM token_usage_overlap_identities;
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=OLD.legacy_seq;
 INSERT OR IGNORE INTO token_usage_dirty VALUES(OLD.native_source,OLD.native_request_id);
END;

CREATE TRIGGER usage_projection_token_usage_legacy_links_delete AFTER DELETE ON token_usage_legacy_links
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty
 SELECT * FROM token_usage_overlap_identities;
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=OLD.legacy_seq;
 INSERT OR IGNORE INTO token_usage_dirty VALUES(OLD.native_source,OLD.native_request_id);
END;

CREATE TRIGGER usage_projection_prices AFTER INSERT ON token_usage_price_catalogs
WHEN NEW.collected_at=(SELECT MAX(collected_at) FROM token_usage_price_catalogs) AND NEW.catalog_json IS NOT (SELECT catalog_json FROM token_usage_price_catalogs WHERE collected_at<NEW.collected_at ORDER BY collected_at DESC LIMIT 1)
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_current;
END;

CREATE TRIGGER usage_projection_runs_insert AFTER INSERT ON runs
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id;
END;

CREATE TRIGGER usage_projection_runs_update AFTER UPDATE OF task_id,role,observed_state ON runs
WHEN OLD.task_id IS NOT NEW.task_id OR OLD.role IS NOT NEW.role OR OLD.observed_state IS NOT NEW.observed_state
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id;
END;

CREATE TRIGGER usage_projection_runs_delete AFTER DELETE ON runs
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id=OLD.run_id;
END;

CREATE TRIGGER usage_projection_tasks_insert AFTER INSERT ON tasks
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE task_id=NEW.task_id;
END;

CREATE TRIGGER usage_projection_tasks_update AFTER UPDATE OF title ON tasks
WHEN OLD.title IS NOT NEW.title
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE task_id=NEW.task_id;
END;

CREATE TRIGGER usage_projection_tasks_delete AFTER DELETE ON tasks
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE task_id=OLD.task_id;
END;

CREATE TRIGGER usage_projection_run_agent_executions_insert AFTER INSERT ON run_agent_executions
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id;
END;

CREATE TRIGGER usage_projection_run_agent_executions_update AFTER UPDATE OF pin_json ON run_agent_executions
WHEN OLD.pin_json IS NOT NEW.pin_json
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id;
END;

CREATE TRIGGER usage_projection_run_agent_executions_delete AFTER DELETE ON run_agent_executions
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id=OLD.run_id;
END;

CREATE TRIGGER usage_projection_task_work_item_owners_insert AFTER INSERT ON task_work_item_owners
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE task_id=NEW.task_id;
END;

CREATE TRIGGER usage_projection_task_work_item_owners_update AFTER UPDATE OF work_item_id ON task_work_item_owners
WHEN OLD.work_item_id IS NOT NEW.work_item_id
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE task_id=NEW.task_id;
END;

CREATE TRIGGER usage_projection_task_work_item_owners_delete AFTER DELETE ON task_work_item_owners
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE task_id=OLD.task_id;
END;

CREATE TRIGGER usage_projection_run_issue_responsibilities_insert AFTER INSERT ON run_issue_responsibilities
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id;
END;

CREATE TRIGGER usage_projection_run_issue_responsibilities_update AFTER UPDATE OF issue_id ON run_issue_responsibilities
WHEN OLD.issue_id IS NOT NEW.issue_id
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id;
END;

CREATE TRIGGER usage_projection_run_issue_responsibilities_delete AFTER DELETE ON run_issue_responsibilities
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id=OLD.run_id;
END;

CREATE TRIGGER usage_projection_work_items_insert AFTER INSERT ON work_items
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected));
END;

CREATE TRIGGER usage_projection_work_items_update AFTER UPDATE OF body_json ON work_items
WHEN json_extract(OLD.body_json,'$.title') IS NOT json_extract(NEW.body_json,'$.title')
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected));
END;

CREATE TRIGGER usage_projection_work_items_delete AFTER DELETE ON work_items
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT OLD.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT OLD.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT OLD.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected));
END;

CREATE TRIGGER usage_projection_issue_controls_insert AFTER INSERT ON issue_controls
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected));
END;

CREATE TRIGGER usage_projection_issue_controls_update AFTER UPDATE OF parent_issue_id ON issue_controls
WHEN OLD.parent_issue_id IS NOT NEW.parent_issue_id
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected));
END;

CREATE TRIGGER usage_projection_issue_controls_delete AFTER DELETE ON issue_controls
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT OLD.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT OLD.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT OLD.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected));
END;

CREATE TRIGGER usage_projection_agent_inventory_versions_insert AFTER INSERT ON agent_inventory_versions
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id IN (SELECT run_id FROM run_agent_executions WHERE json_extract(pin_json,'$.identity.execution_environment_id')=NEW.host_id AND json_extract(pin_json,'$.inventory_revision')=NEW.server_revision);
END;

CREATE TRIGGER usage_projection_agent_inventory_versions_update AFTER UPDATE OF projection_json ON agent_inventory_versions
WHEN OLD.projection_json IS NOT NEW.projection_json
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id IN (SELECT run_id FROM run_agent_executions WHERE json_extract(pin_json,'$.identity.execution_environment_id')=NEW.host_id AND json_extract(pin_json,'$.inventory_revision')=NEW.server_revision);
END;

CREATE TRIGGER usage_projection_agent_inventory_versions_delete AFTER DELETE ON agent_inventory_versions
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE run_id IN (SELECT run_id FROM run_agent_executions WHERE json_extract(pin_json,'$.identity.execution_environment_id')=OLD.host_id AND json_extract(pin_json,'$.inventory_revision')=OLD.server_revision);
END;

CREATE TRIGGER usage_projection_turn_insert AFTER INSERT ON main_turns
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE thread_id=NEW.thread_id AND (turn_id=NEW.turn_id OR root_turn_id=NEW.turn_id);
END;

CREATE TRIGGER usage_projection_turn_update AFTER UPDATE OF thread_id,turn_id,request_id,status ON main_turns
WHEN OLD.thread_id IS NOT NEW.thread_id OR OLD.turn_id IS NOT NEW.turn_id OR OLD.request_id IS NOT NEW.request_id OR OLD.status IS NOT NEW.status
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE thread_id=NEW.thread_id AND (turn_id=NEW.turn_id OR root_turn_id=NEW.turn_id);
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE thread_id=OLD.thread_id AND (turn_id=OLD.turn_id OR root_turn_id=OLD.turn_id);
END;

CREATE TRIGGER usage_projection_turn_delete AFTER DELETE ON main_turns
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE thread_id=OLD.thread_id AND (turn_id=OLD.turn_id OR root_turn_id=OLD.turn_id);
END;

CREATE TRIGGER usage_projection_local_hosted_executions_insert AFTER INSERT ON local_hosted_executions
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE EXISTS (SELECT 1 FROM main_turns t WHERE t.request_id=NEW.execution_id AND t.thread_id=token_usage_contributions.thread_id AND (t.turn_id=token_usage_contributions.turn_id OR t.turn_id=token_usage_contributions.root_turn_id));
END;

CREATE TRIGGER usage_projection_local_hosted_executions_update AFTER UPDATE OF private_snapshot,created_at ON local_hosted_executions
WHEN OLD.private_snapshot IS NOT NEW.private_snapshot OR OLD.created_at IS NOT NEW.created_at
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE EXISTS (SELECT 1 FROM main_turns t WHERE t.request_id=NEW.execution_id AND t.thread_id=token_usage_contributions.thread_id AND (t.turn_id=token_usage_contributions.turn_id OR t.turn_id=token_usage_contributions.root_turn_id));
END;

CREATE TRIGGER usage_projection_local_hosted_executions_delete AFTER DELETE ON local_hosted_executions
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE EXISTS (SELECT 1 FROM main_turns t WHERE t.request_id=OLD.execution_id AND t.thread_id=token_usage_contributions.thread_id AND (t.turn_id=token_usage_contributions.turn_id OR t.turn_id=token_usage_contributions.root_turn_id));
END;

CREATE TRIGGER usage_projection_runner_coordination_executions_insert AFTER INSERT ON runner_coordination_executions
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE EXISTS (SELECT 1 FROM main_turns t WHERE t.request_id=NEW.execution_id AND t.thread_id=token_usage_contributions.thread_id AND (t.turn_id=token_usage_contributions.turn_id OR t.turn_id=token_usage_contributions.root_turn_id));
END;

CREATE TRIGGER usage_projection_runner_coordination_executions_update AFTER UPDATE OF private_snapshot,created_at ON runner_coordination_executions
WHEN OLD.private_snapshot IS NOT NEW.private_snapshot OR OLD.created_at IS NOT NEW.created_at
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE EXISTS (SELECT 1 FROM main_turns t WHERE t.request_id=NEW.execution_id AND t.thread_id=token_usage_contributions.thread_id AND (t.turn_id=token_usage_contributions.turn_id OR t.turn_id=token_usage_contributions.root_turn_id));
END;

CREATE TRIGGER usage_projection_runner_coordination_executions_delete AFTER DELETE ON runner_coordination_executions
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE EXISTS (SELECT 1 FROM main_turns t WHERE t.request_id=OLD.execution_id AND t.thread_id=token_usage_contributions.thread_id AND (t.turn_id=token_usage_contributions.turn_id OR t.turn_id=token_usage_contributions.root_turn_id));
END;

CREATE TRIGGER usage_projection_log_insert AFTER INSERT ON run_logs
WHEN NEW.kind IN ('native.turn_started','native.turn_completed','item.completed')
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE thread_id=json_extract(NEW.body_json,'$.thread_id') AND turn_id=json_extract(NEW.body_json,'$.turn_id');
END;

CREATE TRIGGER usage_projection_log_update AFTER UPDATE OF body_json,kind,run_id ON run_logs
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE thread_id=json_extract(NEW.body_json,'$.thread_id') AND turn_id=json_extract(NEW.body_json,'$.turn_id');
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE thread_id=json_extract(OLD.body_json,'$.thread_id') AND turn_id=json_extract(OLD.body_json,'$.turn_id');
END;

CREATE TRIGGER usage_projection_log_delete AFTER DELETE ON run_logs
WHEN OLD.kind IN ('native.turn_started','native.turn_completed','item.completed')
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE thread_id=json_extract(OLD.body_json,'$.thread_id') AND turn_id=json_extract(OLD.body_json,'$.turn_id');
END;

CREATE TRIGGER usage_projection_actor_insert AFTER INSERT ON token_usage_local_sources
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE conversation_id=NEW.conversation_id;
END;

CREATE TRIGGER usage_projection_actor_update AFTER UPDATE OF source_json,conversation_id ON token_usage_local_sources
WHEN OLD.source_json IS NOT NEW.source_json OR OLD.conversation_id IS NOT NEW.conversation_id
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE conversation_id=NEW.conversation_id;
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE conversation_id=OLD.conversation_id;
END;

CREATE TRIGGER usage_projection_actor_delete AFTER DELETE ON token_usage_local_sources
BEGIN
 INSERT OR IGNORE INTO token_usage_dirty SELECT source,request_id FROM token_usage_contributions WHERE conversation_id=OLD.conversation_id;
END;
