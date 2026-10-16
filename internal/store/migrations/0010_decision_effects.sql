-- What tend does itself when an option of a decision it opened is chosen,
-- and the task's control revision when it asked (D53).
ALTER TABLE decisions ADD COLUMN effects_json TEXT NOT NULL DEFAULT '{}';
ALTER TABLE decisions ADD COLUMN asked_revision INTEGER;
