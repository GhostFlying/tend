-- Online catalog entries survive startup configuration imports.
CREATE TABLE online_repositories (
  repo TEXT PRIMARY KEY REFERENCES repos(repo),
  control_revision INTEGER NOT NULL CHECK (control_revision > 0)
);
