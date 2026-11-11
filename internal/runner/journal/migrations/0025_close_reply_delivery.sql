-- This is peer acceptance of the original close reply, not local process exit
-- or confirmation that the peer received the subsequent compact delivery ACK.
-- Historical absence of a pending reply cannot establish acceptance.
ALTER TABLE manager_native_operations ADD COLUMN reply_delivered INTEGER NOT NULL DEFAULT 0 CHECK(reply_delivered IN (0,1));
