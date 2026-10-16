-- The principal of the request that recorded an event, so a delta can say
-- who made a change (D55). Events recorded outside a request have none.
ALTER TABLE events ADD COLUMN actor_json TEXT;
