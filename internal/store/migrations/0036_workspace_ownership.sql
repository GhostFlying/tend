-- Primary responsibility is creation-time provenance, not a mutable task link.
CREATE TABLE task_work_item_owners (
 task_id TEXT PRIMARY KEY REFERENCES tasks(task_id),
 work_item_id TEXT NOT NULL REFERENCES work_items(work_item_id),
 source_spec_version INTEGER NOT NULL CHECK(source_spec_version=1),
 FOREIGN KEY(task_id,source_spec_version) REFERENCES task_specs(task_id,spec_version)
) STRICT;
CREATE INDEX task_work_item_owner_item ON task_work_item_owners(work_item_id);

-- The server freezes the primary item first in the original task spec. Later
-- specs can contain reordered secondary links, so they are not ownership proof.
-- Legacy tasks without this source remain unknown; current links are not used.
INSERT INTO task_work_item_owners(task_id,work_item_id,source_spec_version)
SELECT s.task_id,w.work_item_id,1
FROM task_specs s JOIN work_items w
 ON w.work_item_id=json_extract(s.body_json,'$.shared_context[0].work_item_id')
WHERE s.spec_version=1
 AND json_type(s.body_json,'$.shared_context[0].work_item_id')='text';

ALTER TABLE workspaces ADD COLUMN owner_work_item_id TEXT REFERENCES work_items(work_item_id);
UPDATE workspaces SET owner_work_item_id=(
 SELECT o.work_item_id FROM task_work_item_owners o WHERE o.task_id=workspaces.task_id
);
