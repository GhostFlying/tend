-- Portable extension metadata is versioned independently of Agent definitions.
CREATE TABLE extension_state (
    revision INTEGER PRIMARY KEY CHECK (revision > 0),
    state_json TEXT NOT NULL CHECK (json_valid(state_json)),
    request_id TEXT NOT NULL
);
CREATE TABLE extension_content (
    sha256 TEXT NOT NULL,
    kind TEXT NOT NULL CHECK (kind IN ('skill','plugin')),
    size INTEGER NOT NULL CHECK (size > 0),
    request_id TEXT NOT NULL,
    PRIMARY KEY (sha256,kind)
);
CREATE TABLE extension_releases (
    release_id TEXT PRIMARY KEY,
    manifest_json TEXT NOT NULL CHECK (json_valid(manifest_json)),
    reference_json TEXT NOT NULL CHECK (json_valid(reference_json)),
    request_id TEXT NOT NULL
);

CREATE TABLE extension_conversation_categories (
 conversation_id TEXT PRIMARY KEY,
 category_id TEXT NOT NULL,
 revision INTEGER NOT NULL CHECK (revision > 0)
);
CREATE TABLE extension_resource_versions (
    kind TEXT NOT NULL,
    resource_id TEXT NOT NULL,
    version TEXT NOT NULL,
    resource_json TEXT NOT NULL CHECK (json_valid(resource_json)),
    PRIMARY KEY(kind,resource_id,version)
);
CREATE TABLE extension_binding_versions (
    binding_id TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    binding_json TEXT NOT NULL CHECK (json_valid(binding_json)),
    PRIMARY KEY(binding_id,revision)
);
