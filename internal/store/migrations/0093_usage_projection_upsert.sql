-- Explicit UPSERT clauses retain their policy inside an outer UPSERT.
-- Index native actor dependencies and resolve execution changes from their turn.
ALTER TABLE token_usage_contributions ADD COLUMN parent_thread_id TEXT NOT NULL DEFAULT '';
ALTER TABLE token_usage_contributions ADD COLUMN session_id TEXT NOT NULL DEFAULT '';
UPDATE token_usage_contributions SET parent_thread_id=COALESCE(json_extract(record_json,'$.parent_thread_id'),''),session_id=COALESCE(json_extract(record_json,'$.session_id'),'');
CREATE INDEX token_usage_contribution_root_turn ON token_usage_contributions(thread_id,root_turn_id);
CREATE INDEX token_usage_actor_thread ON token_usage_contributions(conversation_id,thread_id);
CREATE INDEX token_usage_actor_parent ON token_usage_contributions(conversation_id,parent_thread_id);
CREATE INDEX token_usage_actor_session ON token_usage_contributions(conversation_id,session_id);

CREATE TABLE token_usage_overlap_dirty(legacy_seq INTEGER PRIMARY KEY);
CREATE INDEX token_usage_legacy_links_native ON token_usage_legacy_links(native_source,native_request_id,legacy_seq);

DROP TRIGGER usage_projection_current_insert;
CREATE TRIGGER usage_projection_current_insert AFTER INSERT ON token_usage_current
BEGIN
 INSERT INTO token_usage_dirty VALUES(NEW.source,NEW.request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_overlap_dirty SELECT * FROM (
 SELECT legacy_seq FROM token_usage_legacy_candidates WHERE legacy_seq=NEW.fact_seq
 UNION SELECT legacy_seq FROM token_usage_legacy_candidates WHERE native_source=NEW.source AND native_request_id=NEW.request_id
 UNION SELECT legacy_seq FROM token_usage_legacy_links WHERE legacy_seq=NEW.fact_seq
 UNION SELECT legacy_seq FROM token_usage_legacy_links WHERE native_source=NEW.source AND native_request_id=NEW.request_id
 ) WHERE true ON CONFLICT(legacy_seq) DO NOTHING;
END;

DROP TRIGGER usage_projection_binding_insert;
CREATE TRIGGER usage_projection_binding_insert AFTER INSERT ON token_usage_bindings
BEGIN
 INSERT INTO token_usage_dirty VALUES(NEW.source,NEW.request_id)
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_token_usage_legacy_candidates_insert;
CREATE TRIGGER usage_projection_token_usage_legacy_candidates_insert AFTER INSERT ON token_usage_legacy_candidates
BEGIN
 INSERT INTO token_usage_overlap_dirty VALUES(NEW.legacy_seq) ON CONFLICT(legacy_seq) DO NOTHING;
 INSERT INTO token_usage_dirty VALUES(NEW.native_source,NEW.native_request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=NEW.legacy_seq ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_token_usage_legacy_links_insert;
CREATE TRIGGER usage_projection_token_usage_legacy_links_insert AFTER INSERT ON token_usage_legacy_links
BEGIN
 INSERT INTO token_usage_overlap_dirty VALUES(NEW.legacy_seq) ON CONFLICT(legacy_seq) DO NOTHING;
 INSERT INTO token_usage_dirty VALUES(NEW.native_source,NEW.native_request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=NEW.legacy_seq ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_current_update;
CREATE TRIGGER usage_projection_current_update AFTER UPDATE ON token_usage_current
BEGIN
 INSERT INTO token_usage_dirty VALUES(NEW.source,NEW.request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_overlap_dirty SELECT * FROM (
 SELECT legacy_seq FROM token_usage_legacy_candidates WHERE legacy_seq=NEW.fact_seq
 UNION SELECT legacy_seq FROM token_usage_legacy_candidates WHERE native_source=NEW.source AND native_request_id=NEW.request_id
 UNION SELECT legacy_seq FROM token_usage_legacy_links WHERE legacy_seq=NEW.fact_seq
 UNION SELECT legacy_seq FROM token_usage_legacy_links WHERE native_source=NEW.source AND native_request_id=NEW.request_id
 ) WHERE true ON CONFLICT(legacy_seq) DO NOTHING;
 INSERT INTO token_usage_dirty VALUES(OLD.source,OLD.request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_overlap_dirty SELECT * FROM (
 SELECT legacy_seq FROM token_usage_legacy_candidates WHERE legacy_seq=OLD.fact_seq
 UNION SELECT legacy_seq FROM token_usage_legacy_candidates WHERE native_source=OLD.source AND native_request_id=OLD.request_id
 UNION SELECT legacy_seq FROM token_usage_legacy_links WHERE legacy_seq=OLD.fact_seq
 UNION SELECT legacy_seq FROM token_usage_legacy_links WHERE native_source=OLD.source AND native_request_id=OLD.request_id
 ) WHERE true ON CONFLICT(legacy_seq) DO NOTHING;
END;

DROP TRIGGER usage_projection_binding_update;
CREATE TRIGGER usage_projection_binding_update AFTER UPDATE ON token_usage_bindings
BEGIN
 INSERT INTO token_usage_dirty VALUES(NEW.source,NEW.request_id)
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_token_usage_legacy_candidates_update;
CREATE TRIGGER usage_projection_token_usage_legacy_candidates_update AFTER UPDATE ON token_usage_legacy_candidates
BEGIN
 INSERT INTO token_usage_overlap_dirty VALUES(NEW.legacy_seq) ON CONFLICT(legacy_seq) DO NOTHING;
 INSERT INTO token_usage_dirty VALUES(NEW.native_source,NEW.native_request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=NEW.legacy_seq ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_overlap_dirty VALUES(OLD.legacy_seq) ON CONFLICT(legacy_seq) DO NOTHING;
 INSERT INTO token_usage_dirty VALUES(OLD.native_source,OLD.native_request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=OLD.legacy_seq ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_token_usage_legacy_links_update;
CREATE TRIGGER usage_projection_token_usage_legacy_links_update AFTER UPDATE ON token_usage_legacy_links
BEGIN
 INSERT INTO token_usage_overlap_dirty VALUES(NEW.legacy_seq) ON CONFLICT(legacy_seq) DO NOTHING;
 INSERT INTO token_usage_dirty VALUES(NEW.native_source,NEW.native_request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=NEW.legacy_seq ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_overlap_dirty VALUES(OLD.legacy_seq) ON CONFLICT(legacy_seq) DO NOTHING;
 INSERT INTO token_usage_dirty VALUES(OLD.native_source,OLD.native_request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=OLD.legacy_seq ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_current_delete;
CREATE TRIGGER usage_projection_current_delete AFTER DELETE ON token_usage_current
BEGIN
 INSERT INTO token_usage_dirty VALUES(OLD.source,OLD.request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_overlap_dirty SELECT * FROM (
 SELECT legacy_seq FROM token_usage_legacy_candidates WHERE legacy_seq=OLD.fact_seq
 UNION SELECT legacy_seq FROM token_usage_legacy_candidates WHERE native_source=OLD.source AND native_request_id=OLD.request_id
 UNION SELECT legacy_seq FROM token_usage_legacy_links WHERE legacy_seq=OLD.fact_seq
 UNION SELECT legacy_seq FROM token_usage_legacy_links WHERE native_source=OLD.source AND native_request_id=OLD.request_id
 ) WHERE true ON CONFLICT(legacy_seq) DO NOTHING;
END;

DROP TRIGGER usage_projection_binding_delete;
CREATE TRIGGER usage_projection_binding_delete AFTER DELETE ON token_usage_bindings
BEGIN
 INSERT INTO token_usage_dirty VALUES(OLD.source,OLD.request_id)
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_token_usage_legacy_candidates_delete;
CREATE TRIGGER usage_projection_token_usage_legacy_candidates_delete AFTER DELETE ON token_usage_legacy_candidates
BEGIN
 INSERT INTO token_usage_overlap_dirty VALUES(OLD.legacy_seq) ON CONFLICT(legacy_seq) DO NOTHING;
 INSERT INTO token_usage_dirty VALUES(OLD.native_source,OLD.native_request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=OLD.legacy_seq ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_token_usage_legacy_links_delete;
CREATE TRIGGER usage_projection_token_usage_legacy_links_delete AFTER DELETE ON token_usage_legacy_links
BEGIN
 INSERT INTO token_usage_overlap_dirty VALUES(OLD.legacy_seq) ON CONFLICT(legacy_seq) DO NOTHING;
 INSERT INTO token_usage_dirty VALUES(OLD.native_source,OLD.native_request_id) ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT source,request_id FROM token_usage_current WHERE fact_seq=OLD.legacy_seq ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_prices;
CREATE TRIGGER usage_projection_prices AFTER INSERT ON token_usage_price_catalogs
WHEN NEW.collected_at=(SELECT MAX(collected_at) FROM token_usage_price_catalogs) AND NEW.catalog_json IS NOT (SELECT catalog_json FROM token_usage_price_catalogs WHERE collected_at<NEW.collected_at ORDER BY collected_at DESC LIMIT 1)
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_current
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_runs_insert;
CREATE TRIGGER usage_projection_runs_insert AFTER INSERT ON runs
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_runs_update;
CREATE TRIGGER usage_projection_runs_update AFTER UPDATE OF task_id,role,observed_state ON runs
WHEN OLD.task_id IS NOT NEW.task_id OR OLD.role IS NOT NEW.role OR OLD.observed_state IS NOT NEW.observed_state
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_runs_delete;
CREATE TRIGGER usage_projection_runs_delete AFTER DELETE ON runs
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id=OLD.run_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_tasks_insert;
CREATE TRIGGER usage_projection_tasks_insert AFTER INSERT ON tasks
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE task_id=NEW.task_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_tasks_update;
CREATE TRIGGER usage_projection_tasks_update AFTER UPDATE OF title ON tasks
WHEN OLD.title IS NOT NEW.title
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE task_id=NEW.task_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_tasks_delete;
CREATE TRIGGER usage_projection_tasks_delete AFTER DELETE ON tasks
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE task_id=OLD.task_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_run_agent_executions_insert;
CREATE TRIGGER usage_projection_run_agent_executions_insert AFTER INSERT ON run_agent_executions
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_run_agent_executions_update;
CREATE TRIGGER usage_projection_run_agent_executions_update AFTER UPDATE OF pin_json ON run_agent_executions
WHEN OLD.pin_json IS NOT NEW.pin_json
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_run_agent_executions_delete;
CREATE TRIGGER usage_projection_run_agent_executions_delete AFTER DELETE ON run_agent_executions
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id=OLD.run_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_task_work_item_owners_insert;
CREATE TRIGGER usage_projection_task_work_item_owners_insert AFTER INSERT ON task_work_item_owners
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE task_id=NEW.task_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_task_work_item_owners_update;
CREATE TRIGGER usage_projection_task_work_item_owners_update AFTER UPDATE OF work_item_id ON task_work_item_owners
WHEN OLD.work_item_id IS NOT NEW.work_item_id
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE task_id=NEW.task_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_task_work_item_owners_delete;
CREATE TRIGGER usage_projection_task_work_item_owners_delete AFTER DELETE ON task_work_item_owners
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE task_id=OLD.task_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_run_issue_responsibilities_insert;
CREATE TRIGGER usage_projection_run_issue_responsibilities_insert AFTER INSERT ON run_issue_responsibilities
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_run_issue_responsibilities_update;
CREATE TRIGGER usage_projection_run_issue_responsibilities_update AFTER UPDATE OF issue_id ON run_issue_responsibilities
WHEN OLD.issue_id IS NOT NEW.issue_id
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id=NEW.run_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_run_issue_responsibilities_delete;
CREATE TRIGGER usage_projection_run_issue_responsibilities_delete AFTER DELETE ON run_issue_responsibilities
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id=OLD.run_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_work_items_insert;
CREATE TRIGGER usage_projection_work_items_insert AFTER INSERT ON work_items
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_work_items_update;
CREATE TRIGGER usage_projection_work_items_update AFTER UPDATE OF body_json ON work_items
WHEN json_extract(OLD.body_json,'$.title') IS NOT json_extract(NEW.body_json,'$.title')
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_work_items_delete;
CREATE TRIGGER usage_projection_work_items_delete AFTER DELETE ON work_items
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT OLD.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT OLD.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT OLD.work_item_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_issue_controls_insert;
CREATE TRIGGER usage_projection_issue_controls_insert AFTER INSERT ON issue_controls
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_issue_controls_update;
CREATE TRIGGER usage_projection_issue_controls_update AFTER UPDATE OF parent_issue_id ON issue_controls
WHEN OLD.parent_issue_id IS NOT NEW.parent_issue_id
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT NEW.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_issue_controls_delete;
CREATE TRIGGER usage_projection_issue_controls_delete AFTER DELETE ON issue_controls
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions
 WHERE conversation_id IN (SELECT 'issue:'||id FROM (WITH RECURSIVE affected(id) AS (SELECT OLD.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR task_id IN (SELECT task_id FROM task_work_item_owners WHERE work_item_id IN (WITH RECURSIVE affected(id) AS (SELECT OLD.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 OR run_id IN (SELECT run_id FROM run_issue_responsibilities WHERE issue_id IN (WITH RECURSIVE affected(id) AS (SELECT OLD.issue_id UNION SELECT i.issue_id FROM issue_controls i JOIN affected a ON i.parent_issue_id=a.id) SELECT id FROM affected))
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_agent_inventory_versions_insert;
CREATE TRIGGER usage_projection_agent_inventory_versions_insert AFTER INSERT ON agent_inventory_versions
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id IN (SELECT run_id FROM run_agent_executions WHERE json_extract(pin_json,'$.identity.execution_environment_id')=NEW.host_id AND json_extract(pin_json,'$.inventory_revision')=NEW.server_revision)
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_agent_inventory_versions_update;
CREATE TRIGGER usage_projection_agent_inventory_versions_update AFTER UPDATE OF projection_json ON agent_inventory_versions
WHEN OLD.projection_json IS NOT NEW.projection_json
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id IN (SELECT run_id FROM run_agent_executions WHERE json_extract(pin_json,'$.identity.execution_environment_id')=NEW.host_id AND json_extract(pin_json,'$.inventory_revision')=NEW.server_revision)
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_agent_inventory_versions_delete;
CREATE TRIGGER usage_projection_agent_inventory_versions_delete AFTER DELETE ON agent_inventory_versions
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE run_id IN (SELECT run_id FROM run_agent_executions WHERE json_extract(pin_json,'$.identity.execution_environment_id')=OLD.host_id AND json_extract(pin_json,'$.inventory_revision')=OLD.server_revision)
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_turn_insert;
CREATE TRIGGER usage_projection_turn_insert AFTER INSERT ON main_turns
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE thread_id=NEW.thread_id AND turn_id=NEW.turn_id UNION SELECT source,request_id FROM token_usage_contributions WHERE thread_id=NEW.thread_id AND root_turn_id=NEW.turn_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_turn_update;
CREATE TRIGGER usage_projection_turn_update AFTER UPDATE OF thread_id,turn_id,request_id,status ON main_turns
WHEN OLD.thread_id IS NOT NEW.thread_id OR OLD.turn_id IS NOT NEW.turn_id OR OLD.request_id IS NOT NEW.request_id OR OLD.status IS NOT NEW.status
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE thread_id=NEW.thread_id AND turn_id=NEW.turn_id UNION SELECT source,request_id FROM token_usage_contributions WHERE thread_id=NEW.thread_id AND root_turn_id=NEW.turn_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE thread_id=OLD.thread_id AND turn_id=OLD.turn_id UNION SELECT source,request_id FROM token_usage_contributions WHERE thread_id=OLD.thread_id AND root_turn_id=OLD.turn_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_turn_delete;
CREATE TRIGGER usage_projection_turn_delete AFTER DELETE ON main_turns
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE thread_id=OLD.thread_id AND turn_id=OLD.turn_id UNION SELECT source,request_id FROM token_usage_contributions WHERE thread_id=OLD.thread_id AND root_turn_id=OLD.turn_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_local_hosted_executions_insert;
CREATE TRIGGER usage_projection_local_hosted_executions_insert AFTER INSERT ON local_hosted_executions
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.turn_id=t.turn_id WHERE t.request_id=NEW.execution_id
 UNION SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.root_turn_id=t.turn_id WHERE t.request_id=NEW.execution_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_local_hosted_executions_update;
CREATE TRIGGER usage_projection_local_hosted_executions_update AFTER UPDATE OF private_snapshot,created_at ON local_hosted_executions
WHEN OLD.private_snapshot IS NOT NEW.private_snapshot OR OLD.created_at IS NOT NEW.created_at
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.turn_id=t.turn_id WHERE t.request_id=NEW.execution_id
 UNION SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.root_turn_id=t.turn_id WHERE t.request_id=NEW.execution_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_local_hosted_executions_delete;
CREATE TRIGGER usage_projection_local_hosted_executions_delete AFTER DELETE ON local_hosted_executions
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.turn_id=t.turn_id WHERE t.request_id=OLD.execution_id
 UNION SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.root_turn_id=t.turn_id WHERE t.request_id=OLD.execution_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_runner_coordination_executions_insert;
CREATE TRIGGER usage_projection_runner_coordination_executions_insert AFTER INSERT ON runner_coordination_executions
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.turn_id=t.turn_id WHERE t.request_id=NEW.execution_id
 UNION SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.root_turn_id=t.turn_id WHERE t.request_id=NEW.execution_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_runner_coordination_executions_update;
CREATE TRIGGER usage_projection_runner_coordination_executions_update AFTER UPDATE OF private_snapshot,created_at ON runner_coordination_executions
WHEN OLD.private_snapshot IS NOT NEW.private_snapshot OR OLD.created_at IS NOT NEW.created_at
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.turn_id=t.turn_id WHERE t.request_id=NEW.execution_id
 UNION SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.root_turn_id=t.turn_id WHERE t.request_id=NEW.execution_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_runner_coordination_executions_delete;
CREATE TRIGGER usage_projection_runner_coordination_executions_delete AFTER DELETE ON runner_coordination_executions
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.turn_id=t.turn_id WHERE t.request_id=OLD.execution_id
 UNION SELECT p.source,p.request_id FROM main_turns t CROSS JOIN token_usage_contributions p
 ON p.thread_id=t.thread_id AND p.root_turn_id=t.turn_id WHERE t.request_id=OLD.execution_id
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_log_insert;
CREATE TRIGGER usage_projection_log_insert AFTER INSERT ON run_logs
WHEN NEW.kind IN ('native.turn_started','native.turn_completed','item.completed')
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE thread_id=json_extract(NEW.body_json,'$.thread_id') AND turn_id=json_extract(NEW.body_json,'$.turn_id')
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_log_update;
CREATE TRIGGER usage_projection_log_update AFTER UPDATE OF body_json,kind,run_id ON run_logs
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE thread_id=json_extract(NEW.body_json,'$.thread_id') AND turn_id=json_extract(NEW.body_json,'$.turn_id')
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE thread_id=json_extract(OLD.body_json,'$.thread_id') AND turn_id=json_extract(OLD.body_json,'$.turn_id')
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_log_delete;
CREATE TRIGGER usage_projection_log_delete AFTER DELETE ON run_logs
WHEN OLD.kind IN ('native.turn_started','native.turn_completed','item.completed')
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE thread_id=json_extract(OLD.body_json,'$.thread_id') AND turn_id=json_extract(OLD.body_json,'$.turn_id')
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_actor_insert;
CREATE TRIGGER usage_projection_actor_insert AFTER INSERT ON token_usage_local_sources
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=NEW.conversation_id AND thread_id=json_extract(NEW.source_json,'$.thread_id')
 UNION SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=NEW.conversation_id AND parent_thread_id=json_extract(NEW.source_json,'$.thread_id')
 UNION SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=NEW.conversation_id AND session_id=json_extract(NEW.source_json,'$.session_id')
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_actor_update;
CREATE TRIGGER usage_projection_actor_update AFTER UPDATE OF source_json,conversation_id ON token_usage_local_sources
WHEN OLD.source_json IS NOT NEW.source_json OR OLD.conversation_id IS NOT NEW.conversation_id
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=NEW.conversation_id AND thread_id=json_extract(NEW.source_json,'$.thread_id')
 UNION SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=NEW.conversation_id AND parent_thread_id=json_extract(NEW.source_json,'$.thread_id')
 UNION SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=NEW.conversation_id AND session_id=json_extract(NEW.source_json,'$.session_id')
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=OLD.conversation_id AND thread_id=json_extract(OLD.source_json,'$.thread_id')
 UNION SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=OLD.conversation_id AND parent_thread_id=json_extract(OLD.source_json,'$.thread_id')
 UNION SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=OLD.conversation_id AND session_id=json_extract(OLD.source_json,'$.session_id')
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;

DROP TRIGGER usage_projection_actor_delete;
CREATE TRIGGER usage_projection_actor_delete AFTER DELETE ON token_usage_local_sources
BEGIN
 INSERT INTO token_usage_dirty SELECT * FROM (
 SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=OLD.conversation_id AND thread_id=json_extract(OLD.source_json,'$.thread_id')
 UNION SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=OLD.conversation_id AND parent_thread_id=json_extract(OLD.source_json,'$.thread_id')
 UNION SELECT source,request_id FROM token_usage_contributions WHERE native=1 AND conversation_id=OLD.conversation_id AND session_id=json_extract(OLD.source_json,'$.session_id')
 ) WHERE true
 ON CONFLICT(source,request_id) DO NOTHING;
END;
