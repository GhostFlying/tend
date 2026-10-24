CREATE TABLE work_items (
 work_item_id TEXT PRIMARY KEY,
 project TEXT NOT NULL REFERENCES projects(project),
 revision INTEGER NOT NULL,
 status TEXT NOT NULL CHECK(status IN ('open','snoozed','closed')),
 body_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
CREATE INDEX work_items_status ON work_items(status,updated_at);
CREATE TABLE work_item_revisions (
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 revision INTEGER NOT NULL,
 body_json TEXT NOT NULL,
 reason_json TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(work_item_id,revision)
) STRICT;
CREATE TABLE work_item_task_links (
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 stage_id TEXT NOT NULL,
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 PRIMARY KEY(work_item_id,stage_id,task_id)
) STRICT;
CREATE INDEX work_item_tasks_task ON work_item_task_links(task_id);
