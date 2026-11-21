-- Bound message-first IM processing reads by the inputs on the current page.
CREATE INDEX logical_turn_messages_by_message ON logical_turn_messages(message_id,logical_turn_id);
CREATE INDEX main_steers_by_message ON main_steers(message_id,thread_id,turn_id);
