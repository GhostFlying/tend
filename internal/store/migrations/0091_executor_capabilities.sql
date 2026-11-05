-- Execution capabilities are independent of role categories and adapter readiness.
CREATE TABLE capability_definition_versions (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 capability_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK(revision > 0),
 definition_json TEXT NOT NULL CHECK(json_valid(definition_json)),
 request_id TEXT NOT NULL,
 UNIQUE(capability_id,revision)
) STRICT;
INSERT INTO capability_definition_versions(capability_id,revision,definition_json,request_id)
 VALUES('image_gen',1,'{"capability_id":"image_gen","revision":1,"title":"Image generation","description":"Tool exposure at one exact executor; successful generation is separate execution evidence.","state":"active","contract_version":1,"verification_policy":"tool_inventory_v1","tool_name":"image_gen__imagegen"}','builtin-image-gen-v1');

CREATE TABLE executor_capability_evidence (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 evidence_id TEXT NOT NULL UNIQUE,
 host_id TEXT NOT NULL REFERENCES hosts(host_id),
 agent_id TEXT NOT NULL,
 capability_id TEXT NOT NULL,
 definition_revision INTEGER NOT NULL,
 evidence_json TEXT NOT NULL CHECK(json_valid(evidence_json))
) STRICT;
CREATE INDEX executor_capability_evidence_agent ON executor_capability_evidence(host_id,agent_id,sequence);
CREATE TABLE run_capability_bindings (
 run_id TEXT PRIMARY KEY REFERENCES runs(run_id),
 binding_json TEXT NOT NULL CHECK(json_valid(binding_json))
) STRICT;
CREATE TRIGGER capability_definition_versions_immutable BEFORE UPDATE ON capability_definition_versions
 BEGIN SELECT RAISE(ABORT,'capability definition history is immutable'); END;
CREATE TRIGGER executor_capability_evidence_immutable BEFORE UPDATE ON executor_capability_evidence
 BEGIN SELECT RAISE(ABORT,'capability evidence is immutable'); END;
CREATE TRIGGER run_capability_bindings_immutable BEFORE UPDATE ON run_capability_bindings
 BEGIN SELECT RAISE(ABORT,'run capability binding is immutable'); END;

CREATE TABLE capability_wait_notices (
 task_id TEXT NOT NULL REFERENCES tasks(task_id),
 spec_version INTEGER NOT NULL,
 role TEXT NOT NULL,
 request_id TEXT NOT NULL,
 created_at INTEGER NOT NULL,
 PRIMARY KEY(task_id,spec_version,role)
) STRICT;

CREATE TABLE run_capability_revalidations (
 sequence INTEGER PRIMARY KEY AUTOINCREMENT,
 run_id TEXT NOT NULL REFERENCES runs(run_id),
 request_id TEXT NOT NULL,
 revalidation_json TEXT NOT NULL CHECK(json_valid(revalidation_json)),
 UNIQUE(run_id,request_id)
) STRICT;
CREATE TRIGGER run_capability_revalidations_immutable BEFORE UPDATE ON run_capability_revalidations
 BEGIN SELECT RAISE(ABORT,'run capability revalidation is immutable'); END;
