-- Restart-safe polling, instance metadata and immutable rule decisions.
CREATE TABLE feed_runtime_state (
    feed_id TEXT PRIMARY KEY REFERENCES feeds(feed_id) ON DELETE CASCADE,
    initialized INTEGER NOT NULL DEFAULT 0 CHECK (initialized IN (0,1)),
    last_polled_at TEXT NOT NULL,
    last_success_at TEXT,
    last_error_code TEXT,
    last_entries INTEGER NOT NULL DEFAULT 0,
    last_matched INTEGER NOT NULL DEFAULT 0
);
CREATE TABLE instance_metadata (
    instance_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL CHECK (enabled IN (0,1)),
    updated_at TEXT NOT NULL
);
CREATE TABLE job_rule_snapshots (
    job_id TEXT PRIMARY KEY REFERENCES race_jobs(job_id) ON DELETE CASCADE,
    aggregation_json TEXT NOT NULL CHECK (json_valid(aggregation_json)),
    content_size INTEGER NOT NULL CHECK (content_size >= 0),
    created_at TEXT NOT NULL
);
CREATE TRIGGER job_rule_snapshots_immutable BEFORE UPDATE ON job_rule_snapshots
BEGIN SELECT RAISE(ABORT, 'JOB_RULE_SNAPSHOT_IMMUTABLE'); END;
