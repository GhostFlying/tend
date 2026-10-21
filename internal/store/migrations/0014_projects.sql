-- Projects (D52, D58): tend's own grouping of tasks, defined in the server
-- configuration and synced at startup. A project dropped from the
-- configuration is retired, not deleted, since tasks refer to it.
CREATE TABLE projects (
  project    TEXT    NOT NULL PRIMARY KEY,
  title      TEXT    NOT NULL,
  -- Multiplies the rating in the queue order (D60).
  weight     INTEGER NOT NULL CHECK (weight > 0),
  -- Logical names of the repositories the project works in.
  repos_json TEXT    NOT NULL,
  -- Workspace template for tasks created without a workspace.
  template   TEXT,
  retired    INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

-- Every task belongs to one project. Tasks from before projects belong to
-- the implicit default project, which the next config sync fills in or
-- retires.
INSERT INTO projects (project, title, weight, repos_json, created_at, updated_at)
  VALUES ('default', 'Default', 1, '[]', 0, 0);
-- SQLite refuses a REFERENCES clause on an added column with a default, so
-- the app checks the project exists.
ALTER TABLE tasks ADD COLUMN project TEXT NOT NULL DEFAULT 'default';
CREATE INDEX tasks_project ON tasks (project);
