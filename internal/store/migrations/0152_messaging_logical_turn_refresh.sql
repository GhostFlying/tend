-- A shared logical turn has one coalesced IM refresh, independent of the path
-- that changes its state. Adopted turn rows point to the new active scope.
CREATE TRIGGER messaging_logical_turn_refresh
AFTER UPDATE OF status, resumed_by ON logical_turns
WHEN (
 (NEW.resumed_by IS NULL AND OLD.status IS NOT NEW.status)
 OR (NEW.resumed_by IS NOT NULL AND OLD.resumed_by IS NOT NEW.resumed_by)
)
AND EXISTS (
 SELECT 1 FROM logical_turn_messages
 WHERE logical_turn_id=COALESCE(NEW.resumed_by,NEW.logical_turn_id)
 LIMIT 1 OFFSET 1
)
AND EXISTS (
 SELECT 1 FROM logical_turn_messages l JOIN messaging_messages m USING(message_id)
 WHERE l.logical_turn_id=COALESCE(NEW.resumed_by,NEW.logical_turn_id) AND m.direction='input'
)
BEGIN
 -- Reserve the same ordered cursor as advanceRefresh. This notice only
 -- invalidates IM, so its supersession does not expire the Chat cursor.
 UPDATE refresh_stream_state
 SET cursor=cursor+1, expired_through=MAX(expired_through,COALESCE((
   SELECT event_seq FROM refresh_notifications
   WHERE kind='messaging.turn_processing' AND object_type='logical_turn'
     AND object_id=COALESCE(NEW.resumed_by,NEW.logical_turn_id)
 ),0)) WHERE singleton=1;
 UPDATE sqlite_sequence SET seq=MAX(seq,(SELECT cursor FROM refresh_stream_state WHERE singleton=1))
 WHERE name='events';
 INSERT INTO refresh_notifications(kind,object_type,object_id,revision,event_seq,pending,changed_at)
 SELECT 'messaging.turn_processing','logical_turn',COALESCE(NEW.resumed_by,NEW.logical_turn_id),1,cursor,1,NEW.updated_at
 FROM refresh_stream_state WHERE singleton=1
 ON CONFLICT(kind,object_type,object_id) DO UPDATE
 SET revision=revision+1,event_seq=excluded.event_seq,pending=1,changed_at=excluded.changed_at;
END;
