-- Task references preserve follow-up provenance without reopening outcomes (D77).
CREATE TABLE task_relations (
  source_task_id TEXT NOT NULL REFERENCES tasks(task_id),
  target_task_id TEXT NOT NULL REFERENCES tasks(task_id),
  kind TEXT NOT NULL CHECK (kind IN ('follow_up', 'related')),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (source_task_id, kind, target_task_id),
  CHECK (source_task_id <> target_task_id)
) STRICT;
CREATE INDEX task_relations_target ON task_relations (target_task_id);
