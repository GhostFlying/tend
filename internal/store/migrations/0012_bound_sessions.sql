-- When an agent session first called a tend tool (D54). Only such sessions
-- get context from the hooks. Sessions recorded before are taken as bound,
-- so a control session in use keeps its context across the upgrade.
ALTER TABLE agent_sessions ADD COLUMN bound_at INTEGER;
UPDATE agent_sessions SET bound_at = first_seen_at;
