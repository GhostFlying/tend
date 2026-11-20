-- Worker reply fragments are process records. Their contents and the original
-- sequence stay available, including for a close frozen before this migration.
UPDATE manager_native_events AS e SET applied=1
WHERE applied=0
 AND EXISTS(SELECT 1 FROM runs r JOIN issue_manager_tasks m USING(task_id)
   WHERE r.run_id=e.run_id AND r.role='issue_manager' AND m.conversation_id NOT IN ('','main'))
 AND json_extract(body_json,'$.native.Kind')='agent_message_delta'
 AND COALESCE(json_extract(body_json,'$.native.ItemType'),'') IN ('','agentMessage')
 AND COALESCE(json_extract(body_json,'$.thread_id'),'')<>''
 AND COALESCE(json_extract(body_json,'$.native.TurnID'),'')<>''
 AND COALESCE(json_extract(body_json,'$.native.ItemID'),'')<>''
 AND json_type(body_json,'$.native.Text')='text'
 AND COALESCE(json_extract(body_json,'$.disconnected'),0)=0
 AND json_extract(body_json,'$.call') IS NULL
 AND json_extract(body_json,'$.native.Tool') IS NULL
 AND json_extract(body_json,'$.native.RequestUsage') IS NULL
 AND json_extract(body_json,'$.native.ContextUsage') IS NULL
 AND COALESCE(json_extract(body_json,'$.native.TurnStatus'),'')=''
 AND json_extract(body_json,'$.native.Failure') IS NULL;
