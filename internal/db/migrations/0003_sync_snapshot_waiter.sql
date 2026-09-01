CREATE TABLE sync_instance_state (
    instance_id TEXT PRIMARY KEY,
    sync_state TEXT NOT NULL DEFAULT 'RECONCILING' CHECK (sync_state IN ('RECONCILING', 'READY', 'STALE', 'OFFLINE')),
    sync_generation INTEGER NOT NULL DEFAULT 0 CHECK (sync_generation >= 0),
    rid INTEGER NOT NULL DEFAULT 0 CHECK (rid >= 0),
    snapshot_revision INTEGER NOT NULL DEFAULT 0 CHECK (snapshot_revision >= 0),
    snapshot_digest TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE sync_snapshots (
    instance_id TEXT NOT NULL,
    sync_generation INTEGER NOT NULL CHECK (sync_generation >= 0),
    snapshot_revision INTEGER NOT NULL CHECK (snapshot_revision >= 0),
    rid INTEGER NOT NULL CHECK (rid >= 0),
    snapshot_digest TEXT NOT NULL,
    snapshot_path TEXT NOT NULL,
    is_complete INTEGER NOT NULL CHECK (is_complete IN (0, 1)),
    created_at TEXT NOT NULL,
    PRIMARY KEY (instance_id, sync_generation, snapshot_revision),
    FOREIGN KEY (instance_id) REFERENCES sync_instance_state(instance_id) ON DELETE CASCADE
);

CREATE TABLE sync_waiters (
    waiter_id TEXT PRIMARY KEY,
    attempt_id TEXT NOT NULL UNIQUE,
    instance_id TEXT NOT NULL,
    registered_generation INTEGER NOT NULL CHECK (registered_generation >= 0),
    source_snapshot_revision INTEGER NOT NULL CHECK (source_snapshot_revision >= 0),
    identity_key TEXT NOT NULL,
    confirmation_deadline TEXT NOT NULL,
    reconciliation_deadline TEXT NOT NULL,
    waiter_state TEXT NOT NULL DEFAULT 'ACTIVE' CHECK (waiter_state IN ('ACTIVE', 'CLOSED')),
    created_at TEXT NOT NULL,
    closed_at TEXT,
    FOREIGN KEY (instance_id, registered_generation, source_snapshot_revision)
        REFERENCES sync_snapshots(instance_id, sync_generation, snapshot_revision),
    CHECK (reconciliation_deadline >= confirmation_deadline),
    CHECK ((waiter_state = 'ACTIVE' AND closed_at IS NULL) OR (waiter_state = 'CLOSED' AND closed_at IS NOT NULL))
);

CREATE INDEX sync_snapshots_instance_revision_idx
    ON sync_snapshots(instance_id, sync_generation DESC, snapshot_revision DESC);
CREATE INDEX sync_waiters_active_instance_idx
    ON sync_waiters(instance_id, registered_generation)
    WHERE waiter_state = 'ACTIVE';