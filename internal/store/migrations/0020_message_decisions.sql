-- The decision option an owner message chooses, for options tend does not
-- carry out itself; the main agent records the answer (D71).
ALTER TABLE conversation_messages ADD COLUMN decision_json TEXT;
