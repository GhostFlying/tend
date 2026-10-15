-- Context packets injected into harness threads and each thread's
-- baseline: how much of the ledger the thread's model has seen (D17, D18).

-- status: built (persisted, not yet injected), injected (injected and the
-- turn that follows started), failed (certainly not injected), unknown
-- (injection outcome not known).
CREATE TABLE context_packets (
  packet_id  TEXT    NOT NULL PRIMARY KEY,
  thread_key TEXT    NOT NULL,
  epoch      INTEGER NOT NULL,
  kind       TEXT    NOT NULL,
  from_seq   INTEGER NOT NULL,
  to_seq     INTEGER NOT NULL,
  body       TEXT    NOT NULL,
  status     TEXT    NOT NULL,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
) STRICT;

CREATE INDEX context_packets_thread ON context_packets (thread_key, created_at);

-- watermark_seq is the last event the thread's model has seen; valid = 0
-- means the next turn needs a full snapshot.
CREATE TABLE thread_baselines (
  thread_key     TEXT    NOT NULL PRIMARY KEY,
  epoch          INTEGER NOT NULL,
  watermark_seq  INTEGER NOT NULL,
  last_packet_id TEXT,
  valid          INTEGER NOT NULL,
  updated_at     INTEGER NOT NULL
) STRICT;
