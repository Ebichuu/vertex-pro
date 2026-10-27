-- Deletions qui performed, imported from its automation activity log.
-- Keyed by qui's activity ID per instance so re-imports are idempotent.
-- Dry-run entries are never imported.
CREATE TABLE deletion_audit (
    instance_id TEXT NOT NULL,
    qui_activity_id INTEGER NOT NULL CHECK (qui_activity_id > 0),
    hash TEXT NOT NULL,
    torrent_name TEXT NOT NULL DEFAULT '',
    tracker_domain TEXT NOT NULL DEFAULT '',
    action TEXT NOT NULL,
    outcome TEXT NOT NULL CHECK (outcome IN ('success', 'failed')),
    reason TEXT NOT NULL DEFAULT '',
    qui_rule_id INTEGER,
    qui_rule_name TEXT NOT NULL DEFAULT '',
    -- policy: a bound Vertex deletion policy; legacy_rule: a per-rule
    -- seed-time automation (quipolicy); external: any other qui rule.
    source TEXT NOT NULL CHECK (source IN ('policy', 'legacy_rule', 'external')),
    policy_id TEXT,
    policy_role TEXT,
    policy_revision INTEGER,
    vertex_rule_id TEXT,
    job_id TEXT,
    details_json TEXT NOT NULL DEFAULT '{}' CHECK (json_valid(details_json)),
    occurred_at TEXT NOT NULL,
    imported_at TEXT NOT NULL,
    PRIMARY KEY (instance_id, qui_activity_id)
);
CREATE INDEX ix_deletion_audit_job ON deletion_audit(job_id) WHERE job_id IS NOT NULL;
CREATE INDEX ix_deletion_audit_instance_activity ON deletion_audit(instance_id, qui_activity_id DESC);

-- qui lists activity newest first with no cursor. The highest ID seen per
-- instance lets the importer detect when a full page may have skipped
-- entries (more than one page of activity between imports).
CREATE TABLE deletion_audit_cursor (
    instance_id TEXT PRIMARY KEY,
    max_activity_id INTEGER NOT NULL DEFAULT 0 CHECK (max_activity_id >= 0),
    last_import_at TEXT NOT NULL,
    gap_detected_at TEXT
);
