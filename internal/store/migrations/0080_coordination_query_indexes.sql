-- Keep native close reconciliation and manager handoff checks bounded by
-- the original responsibility, rather than unrelated execution history.
CREATE INDEX runner_coordination_executions_issue
 ON runner_coordination_executions(issue_id,assignment_epoch,execution_id);
CREATE INDEX logical_turn_executions_logical
 ON logical_turn_executions(logical_turn_id,request_id);
CREATE INDEX manager_native_tool_results
 ON manager_native_requests(run_id,
 json_extract(payload_json,'$.thread_id'),
 json_extract(payload_json,'$.turn_id'),
 json_extract(payload_json,'$.call_id'))
 WHERE method='tool_result';
