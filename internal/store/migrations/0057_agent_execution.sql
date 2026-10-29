ALTER TABLE workspaces ADD COLUMN backend_id TEXT NOT NULL DEFAULT '';
CREATE TABLE run_agent_executions (
  run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
  pin_json TEXT NOT NULL
);
