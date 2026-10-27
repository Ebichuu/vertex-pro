-- Deletion policies compiled into qui automations (internal/deletionpolicy).
-- live=1 only after a recorded preview of the exact policy and safety
-- revisions on every target instance; any edit or safety change clears it.
CREATE TABLE deletion_policies (
    policy_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    revision INTEGER NOT NULL CHECK (revision > 0),
    spec_json TEXT NOT NULL CHECK (json_valid(spec_json)),
    instance_ids_json TEXT NOT NULL CHECK (json_valid(instance_ids_json) AND json_type(instance_ids_json) = 'array'),
    live INTEGER NOT NULL DEFAULT 0 CHECK (live IN (0, 1)),
    archived INTEGER NOT NULL DEFAULT 0 CHECK (archived IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK (archived = 0 OR live = 0)
);

CREATE TABLE deletion_safety (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    revision INTEGER NOT NULL CHECK (revision > 0),
    spec_json TEXT NOT NULL CHECK (json_valid(spec_json)),
    updated_at TEXT NOT NULL
);
INSERT INTO deletion_safety(id, revision, spec_json, updated_at) VALUES (1, 1, '{}', strftime('%Y-%m-%dT%H:%M:%SZ', 'now'));

-- Evidence that an operator previewed exactly this policy/safety revision.
CREATE TABLE deletion_previews (
    policy_id TEXT NOT NULL REFERENCES deletion_policies(policy_id),
    policy_revision INTEGER NOT NULL,
    safety_revision INTEGER NOT NULL,
    instance_id TEXT NOT NULL,
    total_matches INTEGER NOT NULL CHECK (total_matches >= 0),
    previewed_at TEXT NOT NULL,
    PRIMARY KEY (policy_id, policy_revision, safety_revision, instance_id)
);

-- The qui automation Vertex owns for one policy role on one instance.
CREATE TABLE deletion_policy_bindings (
    instance_id TEXT NOT NULL,
    policy_id TEXT NOT NULL REFERENCES deletion_policies(policy_id),
    role TEXT NOT NULL CHECK (role IN ('triggers', 'watermark', 'stalled')),
    qui_id INTEGER NOT NULL CHECK (qui_id > 0),
    policy_revision INTEGER NOT NULL,
    safety_revision INTEGER NOT NULL,
    live INTEGER NOT NULL CHECK (live IN (0, 1)),
    payload_json TEXT NOT NULL CHECK (json_valid(payload_json)),
    last_error_code TEXT NOT NULL DEFAULT '',
    updated_at TEXT NOT NULL,
    PRIMARY KEY (instance_id, policy_id, role)
);
