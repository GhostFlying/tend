-- Durable archive admission; queued business remains unchanged.
CREATE TABLE archive_preparation (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 revision INTEGER NOT NULL,
 prepared INTEGER NOT NULL CHECK(prepared IN (0,1)),
 prepared_at INTEGER NOT NULL,
 updated_at INTEGER NOT NULL
) STRICT;
INSERT INTO archive_preparation VALUES(1,0,0,0,0);
