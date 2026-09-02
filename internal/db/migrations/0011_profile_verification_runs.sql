CREATE TABLE profile_verification_runs (
    run_id TEXT PRIMARY KEY,
    campaign_id TEXT NOT NULL,
    repetition INTEGER NOT NULL CHECK (repetition BETWEEN 1 AND 3),
    required_repetitions INTEGER NOT NULL DEFAULT 3 CHECK (required_repetitions = 3),
    instance_id TEXT NOT NULL,
    profile_id TEXT NOT NULL CHECK (profile_id IN ('QB_438', 'QB_504', 'QB_522')),
    source_kind TEXT NOT NULL CHECK (source_kind = 'REAL_PROXY_LAB'),
    qb_app_version TEXT NOT NULL,
    qb_build_fingerprint TEXT NOT NULL,
    qb_artifact_digest TEXT NOT NULL,
    webapi_version TEXT NOT NULL,
    libtorrent_version TEXT NOT NULL,
    qui_version TEXT NOT NULL,
    qui_image_digest TEXT NOT NULL,
    proxy_route_fingerprint TEXT NOT NULL,
    harness_image_digest TEXT NOT NULL,
    vertex_commit TEXT NOT NULL,
    vertex_worktree_clean INTEGER NOT NULL CHECK (vertex_worktree_clean IN (0, 1)),
    test_suite_revision TEXT NOT NULL,
    add_fixture_digest TEXT,
    sync_fixture_digest TEXT,
    tracker_fixture_digest TEXT,
    transfer_info_fixture_digest TEXT,
    verification_report_digest TEXT NOT NULL UNIQUE,
    required_case_count INTEGER NOT NULL CHECK (required_case_count > 0),
    passed_case_count INTEGER NOT NULL CHECK (passed_case_count >= 0 AND passed_case_count <= required_case_count),
    lab_isolated INTEGER NOT NULL CHECK (lab_isolated IN (0, 1)),
    lab_returned_empty INTEGER NOT NULL CHECK (lab_returned_empty IN (0, 1)),
    production_services_called INTEGER NOT NULL CHECK (production_services_called IN (0, 1)),
    secrets_redacted INTEGER NOT NULL CHECK (secrets_redacted IN (0, 1)),
    kill9_case_passed INTEGER NOT NULL CHECK (kill9_case_passed IN (0, 1)),
    automatic_redispatches INTEGER NOT NULL CHECK (automatic_redispatches >= 0),
    verification_state TEXT NOT NULL CHECK (verification_state IN ('VERIFIED', 'FAILED')),
    failure_code TEXT,
    started_at TEXT NOT NULL,
    finished_at TEXT NOT NULL,
    verified_at TEXT,
    created_at TEXT NOT NULL,
    expires_at TEXT NOT NULL,
    CHECK (finished_at >= started_at),
    CHECK (created_at >= finished_at),
    CHECK (expires_at > created_at),
    CHECK (verified_at IS NULL OR (verified_at >= finished_at AND verified_at <= created_at)),
    UNIQUE (campaign_id, profile_id, repetition),
    CHECK (length(qb_build_fingerprint) = 71 AND substr(qb_build_fingerprint, 1, 7) = 'sha256:' AND substr(qb_build_fingerprint, 8) NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(qb_artifact_digest) = 71 AND substr(qb_artifact_digest, 1, 7) = 'sha256:' AND substr(qb_artifact_digest, 8) NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(qui_image_digest) = 71 AND substr(qui_image_digest, 1, 7) = 'sha256:' AND substr(qui_image_digest, 8) NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(proxy_route_fingerprint) = 71 AND substr(proxy_route_fingerprint, 1, 7) = 'sha256:' AND substr(proxy_route_fingerprint, 8) NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(harness_image_digest) = 71 AND substr(harness_image_digest, 1, 7) = 'sha256:' AND substr(harness_image_digest, 8) NOT GLOB '*[^0-9a-f]*'),
    CHECK (length(verification_report_digest) = 71 AND substr(verification_report_digest, 1, 7) = 'sha256:' AND substr(verification_report_digest, 8) NOT GLOB '*[^0-9a-f]*'),
    CHECK (add_fixture_digest IS NULL OR (length(add_fixture_digest) = 71 AND substr(add_fixture_digest, 1, 7) = 'sha256:' AND substr(add_fixture_digest, 8) NOT GLOB '*[^0-9a-f]*')),
    CHECK (sync_fixture_digest IS NULL OR (length(sync_fixture_digest) = 71 AND substr(sync_fixture_digest, 1, 7) = 'sha256:' AND substr(sync_fixture_digest, 8) NOT GLOB '*[^0-9a-f]*')),
    CHECK (tracker_fixture_digest IS NULL OR (length(tracker_fixture_digest) = 71 AND substr(tracker_fixture_digest, 1, 7) = 'sha256:' AND substr(tracker_fixture_digest, 8) NOT GLOB '*[^0-9a-f]*')),
    CHECK (transfer_info_fixture_digest IS NULL OR (length(transfer_info_fixture_digest) = 71 AND substr(transfer_info_fixture_digest, 1, 7) = 'sha256:' AND substr(transfer_info_fixture_digest, 8) NOT GLOB '*[^0-9a-f]*')),
    CHECK (
        (verification_state = 'VERIFIED'
            AND passed_case_count = required_case_count
            AND lab_isolated = 1
            AND lab_returned_empty = 1
            AND production_services_called = 0
            AND secrets_redacted = 1
            AND kill9_case_passed = 1
            AND automatic_redispatches = 0
            AND vertex_worktree_clean = 1
            AND failure_code IS NULL
            AND verified_at IS NOT NULL
            AND add_fixture_digest IS NOT NULL
            AND sync_fixture_digest IS NOT NULL
            AND tracker_fixture_digest IS NOT NULL
            AND transfer_info_fixture_digest IS NOT NULL)
        OR
        (verification_state = 'FAILED'
            AND failure_code IS NOT NULL
            AND verified_at IS NULL
            AND secrets_redacted = 1
            AND production_services_called = 0)
    )
);

CREATE INDEX ix_profile_verification_runs_binding
    ON profile_verification_runs(
        instance_id,
        profile_id,
        campaign_id,
        qb_app_version,
        qb_artifact_digest,
        webapi_version,
        libtorrent_version,
        qui_version,
        test_suite_revision,
        finished_at DESC,
        run_id DESC
    );

CREATE TRIGGER profile_verification_runs_update_guard
BEFORE UPDATE ON profile_verification_runs
BEGIN
    SELECT RAISE(ABORT, 'PROFILE_VERIFICATION_RUN_IMMUTABLE');
END;

CREATE TRIGGER profile_verification_runs_delete_guard
BEFORE DELETE ON profile_verification_runs
BEGIN
    SELECT RAISE(ABORT, 'PROFILE_VERIFICATION_RUN_APPEND_ONLY');
END;
