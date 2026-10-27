-- D113: business-instance identity survives normal restart, not an empty rebuild.
CREATE TABLE deployment_identity (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 deployment_id TEXT NOT NULL UNIQUE
);
