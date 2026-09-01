CREATE TABLE race_jobs (
    job_id TEXT PRIMARY KEY,
    source_id TEXT NOT NULL,
    identity_key TEXT,
    job_state TEXT NOT NULL CHECK (job_state IN ('DISCOVERED', 'PREPARING', 'READY', 'WAITING_CAPACITY', 'PREPARE_FAILED', 'RESERVED', 'ADD_SENT', 'AWAITING_CONFIRMATION', 'HASH_SEEN', 'ALREADY_PRESENT', 'ADD_FAILED', 'UNKNOWN', 'RECONCILED_CONFIRMED', 'RECONCILED_NO_EFFECT', 'EXPIRED_UNKNOWN', 'EXPIRED')),
    rule_state TEXT NOT NULL DEFAULT 'PENDING' CHECK (rule_state IN ('PENDING', 'RUNNING', 'MATCHED', 'REJECTED')),
    torrent_state TEXT NOT NULL DEFAULT 'PENDING' CHECK (torrent_state IN ('PENDING', 'FETCHING', 'READY', 'FAILED')),
    placement_state TEXT NOT NULL DEFAULT 'PENDING' CHECK (placement_state IN ('PENDING', 'PREPARED', 'WAITING_CAPACITY')),
    observation_state TEXT NOT NULL DEFAULT 'NOT_STARTED' CHECK (observation_state IN ('NOT_STARTED', 'ACTIVE', 'CLOSED', 'NOT_APPLICABLE')),
    preparation_revision INTEGER NOT NULL DEFAULT 0 CHECK (preparation_revision >= 0),
    rule_error_code TEXT,
    torrent_error_code TEXT,
    placement_wait_reason TEXT,
    rule_aggregation_revision INTEGER,
    winning_rule_match_id TEXT,
    effective_target_mode TEXT CHECK (effective_target_mode IN ('FIXED_INSTANCE', 'INSTANCE_GROUP')),
    effective_target_instance_id TEXT,
    effective_target_group_id TEXT,
    effective_add_params_json TEXT,
    final_candidate_recheck_revision INTEGER,
    final_candidate_recheck_at TEXT,
    current_attempt_id TEXT REFERENCES placement_attempts(attempt_id),
    participation_deadline TEXT NOT NULL,
    observation_deadline TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((effective_target_mode IS NULL AND effective_target_instance_id IS NULL AND effective_target_group_id IS NULL) OR (effective_target_mode = 'FIXED_INSTANCE' AND effective_target_instance_id IS NOT NULL AND effective_target_group_id IS NULL) OR (effective_target_mode = 'INSTANCE_GROUP' AND effective_target_instance_id IS NULL AND effective_target_group_id IS NOT NULL)),
    CHECK ((job_state IN ('RESERVED', 'ADD_SENT', 'AWAITING_CONFIRMATION', 'UNKNOWN', 'HASH_SEEN', 'RECONCILED_CONFIRMED', 'EXPIRED_UNKNOWN') AND current_attempt_id IS NOT NULL) OR (job_state NOT IN ('RESERVED', 'ADD_SENT', 'AWAITING_CONFIRMATION', 'UNKNOWN', 'HASH_SEEN', 'RECONCILED_CONFIRMED', 'EXPIRED_UNKNOWN') AND current_attempt_id IS NULL))
);

CREATE TABLE race_job_rule_matches (
    rule_match_id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES race_jobs(job_id) ON DELETE CASCADE,
    rule_id TEXT NOT NULL,
    rule_revision INTEGER NOT NULL CHECK (rule_revision > 0),
    match_state TEXT NOT NULL CHECK (match_state IN ('MATCHED', 'REJECTED')),
    aggregation_revision INTEGER NOT NULL CHECK (aggregation_revision >= 0),
    created_at TEXT NOT NULL,
    UNIQUE (job_id, rule_id, rule_revision)
);

CREATE TABLE placement_attempts (
    attempt_id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES race_jobs(job_id) ON DELETE RESTRICT,
    identity_key TEXT NOT NULL,
    attempt_no INTEGER NOT NULL CHECK (attempt_no >= 1),
    instance_id TEXT NOT NULL,
    storage_target_id TEXT NOT NULL,
    attempt_state TEXT NOT NULL CHECK (attempt_state IN ('RESERVED', 'ADD_SENT', 'AWAITING_CONFIRMATION', 'HASH_SEEN', 'ALREADY_PRESENT', 'ADD_FAILED', 'UNKNOWN', 'RECONCILED_CONFIRMED', 'RECONCILED_NO_EFFECT', 'EXPIRED_UNKNOWN')),
    dispatch_state TEXT NOT NULL CHECK (dispatch_state IN ('NOT_STARTED', 'STARTED')),
    side_effect_fact TEXT CHECK (side_effect_fact IS NULL OR side_effect_fact IN ('FAILED_NO_EFFECT', 'POSSIBLE_EFFECT', 'OBSERVED_EFFECT')),
    allocation_epoch INTEGER NOT NULL CHECK (allocation_epoch >= 0),
    qb_torrent_id TEXT,
    confirmation_sync_generation INTEGER CHECK (confirmation_sync_generation IS NULL OR confirmation_sync_generation >= 0),
    confirmation_snapshot_revision INTEGER CHECK (confirmation_snapshot_revision IS NULL OR confirmation_snapshot_revision >= 0),
    confirmation_evidence_digest TEXT,
    reserved_bytes INTEGER NOT NULL CHECK (reserved_bytes >= 0),
    started_at TEXT NOT NULL,
    confirmed_at TEXT,
    finished_at TEXT,
    UNIQUE (job_id, attempt_no),
    CHECK (attempt_state != 'RESERVED' OR dispatch_state = 'NOT_STARTED'),
    CHECK ((attempt_state = 'RESERVED' AND side_effect_fact IS NULL) OR (attempt_state != 'RESERVED' AND side_effect_fact IS NOT NULL)),
    CHECK (attempt_state NOT IN ('HASH_SEEN', 'RECONCILED_CONFIRMED') OR (qb_torrent_id IS NOT NULL AND confirmation_sync_generation IS NOT NULL AND confirmation_snapshot_revision IS NOT NULL AND confirmation_evidence_digest IS NOT NULL AND side_effect_fact = 'OBSERVED_EFFECT'))
);

CREATE TABLE submit_intents (
    submit_intent_id TEXT PRIMARY KEY,
    attempt_id TEXT NOT NULL UNIQUE REFERENCES placement_attempts(attempt_id) ON DELETE RESTRICT,
    instance_id TEXT NOT NULL,
    identity_key TEXT NOT NULL,
    request_fingerprint TEXT NOT NULL,
    target_fingerprint TEXT NOT NULL,
    dispatch_state TEXT NOT NULL CHECK (dispatch_state IN ('NOT_STARTED', 'STARTED')),
    pre_add_sync_generation INTEGER NOT NULL CHECK (pre_add_sync_generation >= 0),
    pre_add_snapshot_revision INTEGER NOT NULL CHECK (pre_add_snapshot_revision >= 0),
    pre_add_snapshot_digest TEXT NOT NULL,
    pre_add_identity_fact TEXT NOT NULL CHECK (pre_add_identity_fact IN ('ABSENT', 'PRESENT')),
    pre_add_qb_torrent_id TEXT,
    created_at TEXT NOT NULL,
    dispatch_started_at TEXT,
    response_fact_at TEXT,
    CHECK ((pre_add_identity_fact = 'ABSENT' AND pre_add_qb_torrent_id IS NULL) OR (pre_add_identity_fact = 'PRESENT' AND pre_add_qb_torrent_id IS NOT NULL)),
    CHECK ((dispatch_state = 'NOT_STARTED' AND dispatch_started_at IS NULL) OR (dispatch_state = 'STARTED' AND dispatch_started_at IS NOT NULL)),
    CHECK (pre_add_identity_fact != 'PRESENT' OR dispatch_state = 'NOT_STARTED')
);

CREATE TRIGGER submit_intent_attempt_match_guard
BEFORE INSERT ON submit_intents
BEGIN
    SELECT RAISE(ABORT, 'SUBMIT_INTENT_ATTEMPT_FACT_MISMATCH')
    WHERE NOT EXISTS (
        SELECT 1 FROM placement_attempts a
        WHERE a.attempt_id = NEW.attempt_id
          AND a.instance_id = NEW.instance_id
          AND a.identity_key = NEW.identity_key
    );
END;

CREATE TRIGGER submit_intent_relation_immutable_guard
BEFORE UPDATE OF attempt_id, instance_id, identity_key ON submit_intents
BEGIN
    SELECT RAISE(ABORT, 'SUBMIT_INTENT_ATTEMPT_RELATION_IMMUTABLE');
END;

CREATE TRIGGER race_job_current_attempt_intent_insert_guard
BEFORE INSERT ON race_jobs
WHEN NEW.current_attempt_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'CURRENT_ATTEMPT_WITHOUT_MATCHING_SUBMIT_INTENT')
    WHERE NOT EXISTS (
        SELECT 1 FROM placement_attempts a
        JOIN submit_intents s ON s.attempt_id = a.attempt_id
        WHERE a.attempt_id = NEW.current_attempt_id
          AND a.job_id = NEW.job_id
    );
END;

CREATE TRIGGER race_job_current_attempt_intent_update_guard
BEFORE UPDATE OF current_attempt_id, job_state ON race_jobs
WHEN NEW.current_attempt_id IS NOT NULL
BEGIN
    SELECT RAISE(ABORT, 'CURRENT_ATTEMPT_WITHOUT_MATCHING_SUBMIT_INTENT')
    WHERE NOT EXISTS (
        SELECT 1 FROM placement_attempts a
        JOIN submit_intents s ON s.attempt_id = a.attempt_id
        WHERE a.attempt_id = NEW.current_attempt_id
          AND a.job_id = NEW.job_id
    );
END;
CREATE TRIGGER capacity_reservation_attempt_guard
BEFORE INSERT ON capacity_reservations
BEGIN
    SELECT RAISE(ABORT, 'RESERVATION_ATTEMPT_NOT_FOUND')
    WHERE NOT EXISTS (SELECT 1 FROM placement_attempts WHERE attempt_id = NEW.attempt_id);
END;

CREATE INDEX race_jobs_state_idx ON race_jobs(job_state, updated_at);
CREATE INDEX placement_attempts_job_idx ON placement_attempts(job_id, attempt_no);
CREATE INDEX submit_intents_dispatch_idx ON submit_intents(dispatch_state, created_at);