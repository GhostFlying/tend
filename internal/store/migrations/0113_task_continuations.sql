ALTER TABLE tasks ADD COLUMN implement_continuations INTEGER NOT NULL DEFAULT 0 CHECK(implement_continuations >= 0);
