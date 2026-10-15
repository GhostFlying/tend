-- Delivery order of the outbox. created_at comes from the request's clock
-- reading, taken before the write transaction, so concurrent requests can
-- commit out of created_at order. seq is assigned inside the enqueue
-- transaction instead; with a single writer it increases with commit order.
ALTER TABLE outbox ADD COLUMN seq INTEGER;
UPDATE outbox SET seq = rowid;
CREATE UNIQUE INDEX outbox_seq ON outbox (seq);
-- The head of each destination's queue.
CREATE INDEX outbox_live ON outbox (destination, seq) WHERE status IN ('pending', 'sending');
