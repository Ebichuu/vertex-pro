CREATE TABLE race_events (
    event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    job_id TEXT NOT NULL REFERENCES race_jobs(job_id) ON DELETE RESTRICT,
    attempt_id TEXT REFERENCES placement_attempts(attempt_id) ON DELETE RESTRICT,
    event_type TEXT NOT NULL CHECK (event_type IN ('job.preparation.changed', 'job.state.changed', 'attempt.state.changed')),
    from_state TEXT,
    to_state TEXT,
    reason_code TEXT,
    safe_metadata_json TEXT NOT NULL DEFAULT '{}',
    occurred_at TEXT NOT NULL,
    CHECK (json_valid(safe_metadata_json))
);

CREATE TABLE event_stream (
    event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL CHECK (event_type IN ('job.preparation.changed', 'job.state.changed', 'attempt.state.changed', 'command.state.changed', 'capacity.state.changed', 'instance.health.changed', 'system.alert')),
    aggregate_type TEXT NOT NULL CHECK (aggregate_type IN ('race_job', 'placement_attempt', 'api_command', 'storage_target', 'instance', 'system')),
    aggregate_id TEXT NOT NULL,
    aggregate_revision INTEGER NOT NULL CHECK (aggregate_revision >= 0),
    safe_data_json TEXT NOT NULL DEFAULT '{}',
    occurred_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    CHECK (json_valid(safe_data_json)),
    CHECK (expires_at > occurred_at)
);

CREATE TABLE api_commands (
    command_id TEXT PRIMARY KEY,
    command_type TEXT NOT NULL,
    subject_id TEXT NOT NULL,
    aggregate_type TEXT,
    aggregate_id TEXT,
    command_status TEXT NOT NULL CHECK (command_status IN ('ACCEPTED', 'RUNNING', 'SUCCEEDED', 'FAILED')),
    request_fingerprint TEXT NOT NULL,
    result_json TEXT,
    result_resource_type TEXT,
    result_resource_id TEXT,
    error_code TEXT,
    error_json TEXT,
    created_at TEXT NOT NULL,
    started_at TEXT,
    finished_at TEXT,
    expires_at TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    CHECK (expires_at > created_at),
    CHECK ((command_status IN ('ACCEPTED', 'RUNNING') AND finished_at IS NULL) OR (command_status = 'SUCCEEDED' AND finished_at IS NOT NULL AND error_code IS NULL) OR (command_status = 'FAILED' AND finished_at IS NOT NULL AND error_code IS NOT NULL)),
    CHECK ((result_json IS NULL OR json_valid(result_json)) AND (error_json IS NULL OR json_valid(error_json)))
);

CREATE TABLE api_idempotency_records (
    idempotency_key TEXT PRIMARY KEY,
    subject_id TEXT NOT NULL,
    request_fingerprint TEXT NOT NULL,
    command_id TEXT NOT NULL UNIQUE REFERENCES api_commands(command_id) ON DELETE RESTRICT,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    CHECK (expires_at > created_at)
);

CREATE TABLE instance_counter_samples (
    sample_id INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id TEXT NOT NULL,
    sampled_at TEXT NOT NULL,
    download_bytes INTEGER NOT NULL CHECK (download_bytes >= 0),
    upload_bytes INTEGER NOT NULL CHECK (upload_bytes >= 0),
    download_rate REAL NOT NULL CHECK (download_rate >= 0),
    upload_rate REAL NOT NULL CHECK (upload_rate >= 0),
    UNIQUE (instance_id, sampled_at)
);

CREATE TABLE instance_traffic_hourly (
    instance_id TEXT NOT NULL,
    bucket_start TEXT NOT NULL,
    download_bytes INTEGER NOT NULL CHECK (download_bytes >= 0),
    upload_bytes INTEGER NOT NULL CHECK (upload_bytes >= 0),
    sample_count INTEGER NOT NULL CHECK (sample_count >= 0),
    revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    updated_at TEXT NOT NULL,
    PRIMARY KEY (instance_id, bucket_start)
);

CREATE TABLE stats_collection_events (
    collection_event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    instance_id TEXT NOT NULL,
    collection_kind TEXT NOT NULL CHECK (collection_kind IN ('COUNTER_SAMPLE', 'TRAFFIC_HOURLY')),
    collected_at TEXT NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('SUCCEEDED', 'FAILED')),
    error_code TEXT,
    CHECK ((status = 'SUCCEEDED' AND error_code IS NULL) OR (status = 'FAILED' AND error_code IS NOT NULL))
);

CREATE TABLE system_events (
    system_event_id INTEGER PRIMARY KEY AUTOINCREMENT,
    event_type TEXT NOT NULL CHECK (event_type IN ('system.alert')),
    severity TEXT NOT NULL CHECK (severity IN ('INFO', 'WARNING', 'ERROR', 'CRITICAL')),
    code TEXT NOT NULL,
    safe_message TEXT NOT NULL,
    safe_data_json TEXT NOT NULL DEFAULT '{}',
    occurred_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    CHECK (json_valid(safe_data_json)),
    CHECK (expires_at > occurred_at)
);

CREATE UNIQUE INDEX uq_event_stream_aggregate_revision ON event_stream(aggregate_type, aggregate_id, aggregate_revision);
CREATE UNIQUE INDEX uq_api_idempotency_subject_key ON api_idempotency_records(subject_id, idempotency_key);
CREATE INDEX ix_event_stream_expires_at ON event_stream(expires_at);
CREATE INDEX ix_event_stream_occurred_at ON event_stream(occurred_at, event_id);
CREATE INDEX ix_api_commands_subject_created ON api_commands(subject_id, created_at DESC);
CREATE INDEX ix_api_commands_expires_at ON api_commands(expires_at);
CREATE INDEX ix_idempotency_expires_at ON api_idempotency_records(expires_at);
CREATE INDEX ix_counter_samples_instance_time ON instance_counter_samples(instance_id, sampled_at DESC);
CREATE INDEX ix_stats_collection_instance_time ON stats_collection_events(instance_id, collected_at DESC);
