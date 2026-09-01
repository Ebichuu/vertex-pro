CREATE TABLE storage_targets (
    storage_target_id TEXT PRIMARY KEY,
    instance_id TEXT NOT NULL,
    target_group TEXT,
    path TEXT NOT NULL,
    storage_verification_state TEXT NOT NULL DEFAULT 'UNVERIFIED'
        CHECK (storage_verification_state IN ('UNVERIFIED', 'VERIFIED')),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (instance_id, path)
);

CREATE TABLE capacity_epochs (
    capacity_epoch_id TEXT PRIMARY KEY,
    storage_target_id TEXT NOT NULL REFERENCES storage_targets(storage_target_id) ON DELETE CASCADE,
    allocation_epoch INTEGER NOT NULL CHECK (allocation_epoch >= 0),
    capacity_state TEXT NOT NULL CHECK (capacity_state IN ('TRUSTED', 'CAPACITY_UNCERTAIN', 'RECONCILING')),
    physical_free_bytes INTEGER CHECK (physical_free_bytes IS NULL OR physical_free_bytes >= 0),
    effective_free_bytes INTEGER CHECK (effective_free_bytes IS NULL OR effective_free_bytes >= 0),
    snapshot_generation INTEGER NOT NULL CHECK (snapshot_generation >= 0),
    snapshot_revision INTEGER NOT NULL CHECK (snapshot_revision >= 0),
    snapshot_digest TEXT NOT NULL,
    created_at TEXT NOT NULL,
    UNIQUE (storage_target_id, allocation_epoch)
);

CREATE TABLE capacity_reservations (
    reservation_id TEXT PRIMARY KEY,
    attempt_id TEXT NOT NULL UNIQUE,
    storage_target_id TEXT NOT NULL REFERENCES storage_targets(storage_target_id),
    reserved_bytes INTEGER NOT NULL CHECK (reserved_bytes >= 0),
    reflected_physical_bytes INTEGER NOT NULL DEFAULT 0
        CHECK (reflected_physical_bytes >= 0 AND reflected_physical_bytes <= reserved_bytes),
    allocation_epoch INTEGER NOT NULL CHECK (allocation_epoch >= 0),
    reservation_state TEXT NOT NULL CHECK (reservation_state IN ('ACTIVE', 'REFLECTING', 'RELEASED')),
    release_reason TEXT CHECK (release_reason IS NULL OR release_reason IN ('NO_EFFECT', 'ALREADY_PRESENT', 'TERMINAL_COMPLETED', 'TERMINAL_QB_REMOVED')),
    released_unconsumed_bytes INTEGER CHECK (released_unconsumed_bytes IS NULL OR released_unconsumed_bytes >= 0),
    released_at TEXT,
    revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    CHECK (
        (reservation_state IN ('ACTIVE', 'REFLECTING') AND release_reason IS NULL AND released_unconsumed_bytes IS NULL AND released_at IS NULL)
        OR
        (reservation_state = 'RELEASED' AND release_reason IS NOT NULL AND released_unconsumed_bytes = reserved_bytes - reflected_physical_bytes AND released_at IS NOT NULL)
    ),
    CHECK (release_reason != 'TERMINAL_COMPLETED' OR (reflected_physical_bytes = reserved_bytes AND released_unconsumed_bytes = 0)),
    CHECK (release_reason NOT IN ('NO_EFFECT', 'ALREADY_PRESENT') OR reflected_physical_bytes = 0),
    CHECK (reservation_state = 'RELEASED' OR reflected_physical_bytes = 0)
);

CREATE TABLE reservation_reflections (
    reflection_id TEXT PRIMARY KEY,
    reservation_id TEXT NOT NULL REFERENCES capacity_reservations(reservation_id) ON DELETE RESTRICT,
    snapshot_generation INTEGER NOT NULL CHECK (snapshot_generation >= 0),
    snapshot_revision INTEGER NOT NULL CHECK (snapshot_revision >= 0),
    snapshot_digest TEXT NOT NULL,
    reflected_physical_bytes INTEGER NOT NULL CHECK (reflected_physical_bytes >= 0),
    observed_at TEXT NOT NULL,
    UNIQUE (reservation_id, snapshot_generation, snapshot_revision)
);

CREATE TABLE capacity_task_bindings (
    binding_id TEXT PRIMARY KEY,
    storage_target_id TEXT NOT NULL REFERENCES storage_targets(storage_target_id),
    qb_torrent_id TEXT NOT NULL,
    identity_key TEXT,
    owner_kind TEXT NOT NULL CHECK (owner_kind IN ('RESERVATION', 'UNMANAGED')),
    reservation_id TEXT REFERENCES capacity_reservations(reservation_id),
    amount_left_state TEXT NOT NULL CHECK (amount_left_state IN ('KNOWN', 'UNKNOWN')),
    amount_left INTEGER CHECK (amount_left IS NULL OR amount_left >= 0),
    amount_left_error_code TEXT CHECK (amount_left_error_code IS NULL OR amount_left_error_code IN ('MISSING_IN_FULL_SNAPSHOT', 'NEGATIVE_VALUE', 'INTEGER_OVERFLOW', 'TYPE_MISMATCH', 'NO_TRUSTED_PRIOR_VALUE', 'SCHEMA_INCOMPATIBLE')),
    uncertainty_debit_kind TEXT NOT NULL DEFAULT 'NONE' CHECK (uncertainty_debit_kind IN ('NONE', 'UNKNOWN_AMOUNT_LEFT_BOUND', 'UNBOUNDED_AMOUNT_LEFT')),
    uncertainty_debit_bytes INTEGER NOT NULL DEFAULT 0 CHECK (uncertainty_debit_bytes >= 0),
    snapshot_generation INTEGER NOT NULL CHECK (snapshot_generation >= 0),
    snapshot_revision INTEGER NOT NULL CHECK (snapshot_revision >= 0),
    snapshot_digest TEXT NOT NULL,
    revision INTEGER NOT NULL DEFAULT 0 CHECK (revision >= 0),
    updated_at TEXT NOT NULL,
    UNIQUE (storage_target_id, qb_torrent_id),
    CHECK ((owner_kind = 'RESERVATION' AND reservation_id IS NOT NULL) OR (owner_kind = 'UNMANAGED' AND reservation_id IS NULL)),
    CHECK ((amount_left_state = 'KNOWN' AND amount_left IS NOT NULL AND amount_left_error_code IS NULL) OR (amount_left_state = 'UNKNOWN' AND amount_left IS NULL AND amount_left_error_code IS NOT NULL)),
    CHECK ((uncertainty_debit_kind = 'NONE' AND uncertainty_debit_bytes = 0) OR (uncertainty_debit_kind = 'UNKNOWN_AMOUNT_LEFT_BOUND' AND uncertainty_debit_bytes > 0) OR (uncertainty_debit_kind = 'UNBOUNDED_AMOUNT_LEFT' AND uncertainty_debit_bytes = 0)),
    CHECK ((amount_left_state = 'KNOWN' AND uncertainty_debit_kind = 'NONE') OR (amount_left_state = 'UNKNOWN' AND owner_kind = 'RESERVATION' AND uncertainty_debit_kind = 'NONE') OR (amount_left_state = 'UNKNOWN' AND owner_kind = 'UNMANAGED' AND uncertainty_debit_kind IN ('UNKNOWN_AMOUNT_LEFT_BOUND', 'UNBOUNDED_AMOUNT_LEFT')))
);

CREATE TRIGGER capacity_binding_reservation_insert_guard
BEFORE INSERT ON capacity_task_bindings
WHEN NEW.owner_kind = 'RESERVATION'
BEGIN
    SELECT RAISE(ABORT, 'RESERVATION_OWNER_NOT_ACTIVE_OR_TARGET_MISMATCH')
    WHERE NOT EXISTS (
        SELECT 1 FROM capacity_reservations r
        WHERE r.reservation_id = NEW.reservation_id
          AND r.storage_target_id = NEW.storage_target_id
          AND r.reservation_state IN ('ACTIVE', 'REFLECTING')
    );
END;

CREATE TRIGGER capacity_binding_reservation_update_guard
BEFORE UPDATE OF owner_kind, reservation_id, storage_target_id ON capacity_task_bindings
WHEN NEW.owner_kind = 'RESERVATION'
BEGIN
    SELECT RAISE(ABORT, 'RESERVATION_OWNER_NOT_ACTIVE_OR_TARGET_MISMATCH')
    WHERE NOT EXISTS (
        SELECT 1 FROM capacity_reservations r
        WHERE r.reservation_id = NEW.reservation_id
          AND r.storage_target_id = NEW.storage_target_id
          AND r.reservation_state IN ('ACTIVE', 'REFLECTING')
    );
END;

CREATE TRIGGER capacity_reservation_owner_guard
BEFORE UPDATE OF reservation_state, storage_target_id ON capacity_reservations
WHEN EXISTS (
    SELECT 1 FROM capacity_task_bindings b
    WHERE b.owner_kind = 'RESERVATION'
      AND b.reservation_id = NEW.reservation_id
      AND (NEW.reservation_state NOT IN ('ACTIVE', 'REFLECTING') OR b.storage_target_id != NEW.storage_target_id)
)
BEGIN
    SELECT RAISE(ABORT, 'RESERVATION_OWNER_STATE_OR_TARGET_INVALID');
END;