-- Host maintenance survives runner restarts; grants require fresh hello proof.
CREATE TABLE runner_control (
 singleton INTEGER PRIMARY KEY CHECK(singleton=1),
 control_json TEXT NOT NULL
) STRICT;

CREATE TABLE updater_authorizations (
    binding_key TEXT PRIMARY KEY,
    envelope_json TEXT NOT NULL
) STRICT;
