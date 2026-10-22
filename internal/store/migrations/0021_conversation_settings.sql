-- Shared owner settings for the next hosted-main turn (D73).
CREATE TABLE conversation_settings (
  singleton INTEGER NOT NULL PRIMARY KEY CHECK (singleton = 1),
  model TEXT NOT NULL,
  effort TEXT NOT NULL,
  revision INTEGER NOT NULL CHECK (revision > 0),
  updated_at INTEGER NOT NULL
) STRICT;

-- Preserve the actual configuration used by a turn, independently of
-- settings changed while it was running.
ALTER TABLE main_turns ADD COLUMN model TEXT NOT NULL DEFAULT '';
ALTER TABLE main_turns ADD COLUMN model_provider TEXT NOT NULL DEFAULT '';
ALTER TABLE main_turns ADD COLUMN effort TEXT NOT NULL DEFAULT '';
ALTER TABLE main_turns ADD COLUMN settings_revision INTEGER NOT NULL DEFAULT 0;
