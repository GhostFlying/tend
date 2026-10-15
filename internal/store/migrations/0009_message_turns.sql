-- Finding a message's turn, to stop a reply in progress (plan C5).
CREATE INDEX main_turns_message ON main_turns (message_id);
