-- D134: immutable question presentations refer to the original business object.
CREATE TABLE messaging_interactions (
 interaction_id TEXT PRIMARY KEY,
 object_type TEXT NOT NULL CHECK(object_type IN ('decision','attention','work_item_choice')),
 object_id TEXT NOT NULL, question_version INTEGER NOT NULL,
 snapshot_json TEXT NOT NULL,
 observed_state TEXT NOT NULL DEFAULT 'open',
 handled_channel TEXT NOT NULL DEFAULT '', handled_at INTEGER NOT NULL DEFAULT 0,
 response_message_id TEXT NOT NULL DEFAULT '', answer_summary TEXT NOT NULL DEFAULT '',
 UNIQUE(object_type,object_id,question_version)
) STRICT;
CREATE TABLE messaging_interaction_messages (
 message_id TEXT PRIMARY KEY REFERENCES conversation_messages(message_id),
 interaction_id TEXT NOT NULL REFERENCES messaging_interactions(interaction_id)
) STRICT;
CREATE INDEX messaging_interaction_object ON messaging_interactions(object_type,object_id);

CREATE TRIGGER messaging_interaction_snapshot_immutable
 BEFORE UPDATE OF object_type,object_id,question_version,snapshot_json ON messaging_interactions
 BEGIN SELECT RAISE(ABORT,'interaction question snapshots are immutable'); END;
CREATE TRIGGER messaging_interaction_origin_immutable
 BEFORE UPDATE OF handled_channel,handled_at,response_message_id ON messaging_interactions
 WHEN OLD.handled_at<>0 AND (NEW.handled_channel<>OLD.handled_channel OR NEW.handled_at<>OLD.handled_at OR NEW.response_message_id<>OLD.response_message_id)
 BEGIN SELECT RAISE(ABORT,'accepted interaction origins are immutable'); END;
