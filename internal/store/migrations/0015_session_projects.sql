-- The project an agent session works in (D59). project_source is auto when
-- tend bound it from the repository the session works in, explicit when the
-- session chose it; an explicit choice without a project means every
-- project.
ALTER TABLE agent_sessions ADD COLUMN project TEXT;
ALTER TABLE agent_sessions ADD COLUMN project_source TEXT;
