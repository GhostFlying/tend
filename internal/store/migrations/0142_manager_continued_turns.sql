-- The logical delivery keeps its root turn. Receipts and steering need the
-- last native turn actually observed in this Run, even after event pruning.
CREATE TABLE manager_continued_turns (
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 thread_id TEXT NOT NULL,
 root_turn TEXT NOT NULL,
 native_turn TEXT NOT NULL,
 awaiting_start INTEGER NOT NULL CHECK(awaiting_start IN (0,1)),
 PRIMARY KEY(run_id,thread_id,root_turn)
) STRICT;
