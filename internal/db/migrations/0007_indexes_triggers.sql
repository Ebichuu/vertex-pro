CREATE UNIQUE INDEX uq_capacity_reservation_attempt
ON capacity_reservations(attempt_id);

CREATE UNIQUE INDEX uq_race_job_current_attempt
ON race_jobs(current_attempt_id)
WHERE current_attempt_id IS NOT NULL;

CREATE UNIQUE INDEX uq_capacity_task_owner
ON capacity_task_bindings(storage_target_id, qb_torrent_id);

ALTER TABLE reservation_reflections ADD COLUMN allocation_epoch INTEGER NOT NULL DEFAULT 0 CHECK (allocation_epoch >= 0);

CREATE UNIQUE INDEX uq_reservation_reflection_step
ON reservation_reflections(reservation_id, allocation_epoch, reflected_physical_bytes);

CREATE UNIQUE INDEX uq_job_active_attempt
ON placement_attempts(job_id)
WHERE attempt_state IN (
    'RESERVED', 'ADD_SENT', 'AWAITING_CONFIRMATION',
    'UNKNOWN', 'HASH_SEEN', 'RECONCILED_CONFIRMED', 'EXPIRED_UNKNOWN'
);

CREATE UNIQUE INDEX uq_identity_active_delivery_chain
ON placement_attempts(identity_key)
WHERE attempt_state IN (
    'RESERVED', 'ADD_SENT', 'AWAITING_CONFIRMATION',
    'UNKNOWN', 'HASH_SEEN', 'RECONCILED_CONFIRMED', 'EXPIRED_UNKNOWN'
);

CREATE UNIQUE INDEX uq_attempt_active_waiter
ON sync_waiters(attempt_id)
WHERE waiter_state = 'ACTIVE';

CREATE INDEX ix_capacity_epochs_target_state
ON capacity_epochs(storage_target_id, capacity_state, allocation_epoch DESC);

CREATE INDEX ix_capacity_reservations_target_state
ON capacity_reservations(storage_target_id, reservation_state, allocation_epoch DESC);

CREATE INDEX ix_capacity_bindings_reservation
ON capacity_task_bindings(reservation_id, owner_kind);

CREATE INDEX ix_sync_waiters_deadlines
ON sync_waiters(waiter_state, confirmation_deadline, reconciliation_deadline);

CREATE INDEX ix_placement_attempts_identity_state
ON placement_attempts(identity_key, attempt_state, started_at DESC);

CREATE INDEX ix_race_events_job_event
ON race_events(job_id, event_id);

CREATE INDEX ix_system_events_expires_at
ON system_events(expires_at);

CREATE TRIGGER placement_attempt_target_immutable_guard
BEFORE UPDATE OF attempt_id, job_id, identity_key, attempt_no, instance_id, storage_target_id, allocation_epoch, reserved_bytes, started_at
ON placement_attempts
BEGIN
    SELECT RAISE(ABORT, 'ATTEMPT_TARGET_IMMUTABLE');
END;

CREATE TRIGGER capacity_reservation_attempt_match_guard
BEFORE INSERT ON capacity_reservations
BEGIN
    SELECT RAISE(ABORT, 'RESERVATION_ATTEMPT_FACT_MISMATCH')
    WHERE NOT EXISTS (
        SELECT 1
        FROM placement_attempts a
        WHERE a.attempt_id = NEW.attempt_id
          AND a.storage_target_id = NEW.storage_target_id
          AND a.allocation_epoch = NEW.allocation_epoch
          AND a.reserved_bytes = NEW.reserved_bytes
    );
END;

CREATE TRIGGER capacity_reservation_attempt_update_guard
BEFORE UPDATE OF attempt_id, storage_target_id, allocation_epoch, reserved_bytes
ON capacity_reservations
BEGIN
    SELECT RAISE(ABORT, 'RESERVATION_ATTEMPT_FACT_IMMUTABLE');
END;

CREATE TRIGGER event_stream_revision_monotonic_guard
BEFORE INSERT ON event_stream
BEGIN
    SELECT RAISE(ABORT, 'EVENT_AGGREGATE_REVISION_NOT_MONOTONIC')
    WHERE NEW.aggregate_revision != COALESCE((
        SELECT MAX(aggregate_revision) + 1
        FROM event_stream
        WHERE aggregate_type = NEW.aggregate_type
          AND aggregate_id = NEW.aggregate_id
    ), 0);
END;

CREATE TRIGGER event_stream_append_only_guard
BEFORE UPDATE ON event_stream
BEGIN
    SELECT RAISE(ABORT, 'EVENT_STREAM_APPEND_ONLY');
END;

CREATE TRIGGER race_events_update_guard
BEFORE UPDATE ON race_events
BEGIN
    SELECT RAISE(ABORT, 'RACE_EVENTS_APPEND_ONLY');
END;

CREATE TRIGGER race_events_delete_guard
BEFORE DELETE ON race_events
BEGIN
    SELECT RAISE(ABORT, 'RACE_EVENTS_APPEND_ONLY');
END;

CREATE TRIGGER system_events_append_only_guard
BEFORE UPDATE ON system_events
BEGIN
    SELECT RAISE(ABORT, 'SYSTEM_EVENTS_APPEND_ONLY');
END;

CREATE TRIGGER api_command_identity_immutable_guard
BEFORE UPDATE OF command_id, subject_id, command_type, request_fingerprint, created_at
ON api_commands
BEGIN
    SELECT RAISE(ABORT, 'API_COMMAND_IDENTITY_IMMUTABLE');
END;

CREATE TRIGGER api_command_state_transition_guard
BEFORE UPDATE OF command_status, revision ON api_commands
BEGIN
    SELECT RAISE(ABORT, 'API_COMMAND_REVISION_NOT_MONOTONIC')
    WHERE NEW.revision != OLD.revision + 1;
    SELECT RAISE(ABORT, 'API_COMMAND_STATE_TRANSITION_INVALID')
    WHERE NOT (
        NEW.command_status = OLD.command_status
        OR (OLD.command_status = 'ACCEPTED' AND NEW.command_status = 'RUNNING')
        OR (OLD.command_status = 'RUNNING' AND NEW.command_status IN ('SUCCEEDED', 'FAILED'))
    );
END;
