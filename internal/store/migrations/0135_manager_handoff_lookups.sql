CREATE INDEX issue_manager_tasks_handoff
 ON issue_manager_tasks(issue_id);

CREATE INDEX manager_native_events_unapplied
 ON manager_native_events(run_id) WHERE applied=0;
