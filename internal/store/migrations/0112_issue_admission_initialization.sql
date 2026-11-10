ALTER TABLE issue_controls ADD COLUMN admission_initialized INTEGER NOT NULL DEFAULT 0 CHECK(admission_initialized IN (0,1));
