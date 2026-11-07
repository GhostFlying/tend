-- Explicit configuration labels, independent of executor/tool observations.
CREATE TABLE capability_definition_versions (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 capability_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision > 0),
 definition_json TEXT NOT NULL CHECK(json_valid(definition_json)),
 request_id TEXT NOT NULL,
 UNIQUE(capability_id,revision)
) STRICT;
INSERT INTO capability_definition_versions(capability_id,revision,definition_json,request_id)
 VALUES('image_gen',1,'{"capability_id":"image_gen","revision":1,"title":"Image generation","description":"User-configured declaration label; does not prove tool availability or execution.","state":"active","contract_version":1,"verification_policy":"declaration_v1"}','builtin-image-gen-declaration-v1');
CREATE TABLE run_capability_bindings (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 binding_json TEXT NOT NULL CHECK(json_valid(binding_json))
) STRICT;
CREATE TABLE capability_wait_notices (
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 spec_version INTEGER NOT NULL,
 role TEXT NOT NULL,
 request_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(task_id,spec_version,role)
) STRICT;
CREATE TRIGGER capability_definition_versions_immutable BEFORE UPDATE ON capability_definition_versions
 BEGIN SELECT RAISE(ABORT,'capability definition history is immutable'); END;
CREATE TRIGGER run_capability_bindings_immutable BEFORE UPDATE ON run_capability_bindings
 BEGIN SELECT RAISE(ABORT,'run capability binding is immutable'); END;
