-- The harness each host runs its workers with (D64). Hosts configured
-- before it run the app-server.
ALTER TABLE hosts ADD COLUMN worker TEXT NOT NULL DEFAULT 'appserver';
