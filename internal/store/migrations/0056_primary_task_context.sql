-- Preserve legacy stage-link invalidation; canonical execution belongs only
-- to its immutable primary issue, including tasks with no stage.
DROP TRIGGER context_task_change;
CREATE TRIGGER context_task_change AFTER UPDATE ON tasks BEGIN
 UPDATE work_item_contexts SET revision=revision+1 WHERE work_item_id IN (
  SELECT o.work_item_id FROM task_work_item_owners o JOIN issue_controls c ON c.issue_id=o.work_item_id WHERE o.task_id=NEW.task_id
  UNION
  SELECT l.work_item_id FROM work_item_task_links l
  WHERE l.task_id=NEW.task_id
   AND NOT EXISTS(SELECT 1 FROM task_work_item_owners o JOIN issue_controls c ON c.issue_id=o.work_item_id WHERE o.task_id=NEW.task_id)
   AND NOT EXISTS(SELECT 1 FROM issue_controls c WHERE c.issue_id=l.work_item_id)
 );
END;
