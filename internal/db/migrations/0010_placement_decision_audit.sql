CREATE TABLE placement_decision_audits (
    audit_id TEXT PRIMARY KEY,
    job_id TEXT REFERENCES race_jobs(job_id) ON DELETE RESTRICT,
    source_id TEXT NOT NULL,
    site_id TEXT NOT NULL REFERENCES sites(site_id) ON DELETE RESTRICT,
    feed_hash TEXT NOT NULL,
    canonical_hash TEXT,
    official_state TEXT NOT NULL CHECK (official_state IN ('OFFICIAL', 'NON_OFFICIAL', 'UNKNOWN')),
    official_reason TEXT NOT NULL,
    algorithm_version TEXT NOT NULL,
    target_mode TEXT NOT NULL CHECK (target_mode IN ('FIXED_INSTANCE', 'INSTANCE_GROUP')),
    selected_instance_id TEXT,
    selected_storage_target_id TEXT,
    selected_score INTEGER,
    decision_state TEXT NOT NULL CHECK (decision_state IN ('SELECTED', 'NO_ELIGIBLE')),
    candidate_snapshot_json TEXT NOT NULL CHECK (json_valid(candidate_snapshot_json)),
    decision_summary_json TEXT NOT NULL CHECK (json_valid(decision_summary_json)),
    add_result_state TEXT NOT NULL DEFAULT 'NOT_ATTEMPTED' CHECK (add_result_state IN ('NOT_ATTEMPTED', 'SUCCEEDED', 'FAILED', 'UNKNOWN')),
    add_result_code TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((decision_state = 'SELECTED' AND selected_instance_id IS NOT NULL AND selected_storage_target_id IS NOT NULL AND selected_score IS NOT NULL) OR (decision_state = 'NO_ELIGIBLE' AND selected_instance_id IS NULL AND selected_storage_target_id IS NULL AND selected_score IS NULL)),
    CHECK (add_result_state = 'NOT_ATTEMPTED' OR add_result_code IS NOT NULL)
);

CREATE INDEX placement_decision_audits_source_idx ON placement_decision_audits(source_id, created_at DESC);
CREATE INDEX placement_decision_audits_site_idx ON placement_decision_audits(site_id, created_at DESC);
CREATE INDEX placement_decision_audits_instance_idx ON placement_decision_audits(selected_instance_id, created_at DESC);
