CREATE TABLE pending_confirmations (
    attempt_id TEXT PRIMARY KEY REFERENCES placement_attempts(attempt_id) ON DELETE RESTRICT,
    evidence_json TEXT NOT NULL CHECK(json_valid(evidence_json))
);

CREATE TABLE dispatch_queue (
    attempt_id TEXT PRIMARY KEY REFERENCES placement_attempts(attempt_id) ON DELETE RESTRICT,
    job_id TEXT NOT NULL REFERENCES race_jobs(job_id) ON DELETE RESTRICT,
    instance_id TEXT NOT NULL,
    params_json TEXT NOT NULL CHECK(json_valid(params_json)),
    queued_at TEXT NOT NULL,
    next_attempt_at TEXT NOT NULL DEFAULT ''
);
CREATE INDEX ix_dispatch_queue_instance ON dispatch_queue(instance_id,queued_at);

ALTER TABLE rss_source_consumers ADD COLUMN discovered_at TEXT NOT NULL DEFAULT '';
ALTER TABLE rss_source_consumers ADD COLUMN matched INTEGER NOT NULL DEFAULT 0 CHECK(matched IN (0,1));

ALTER TABLE job_observations ADD COLUMN first_download_growth_at TEXT;
CREATE TRIGGER job_download_growth_insert AFTER INSERT ON job_observations
WHEN NEW.downloaded>0
BEGIN
 UPDATE job_observations SET first_download_growth_at=NEW.observed_at WHERE job_id=NEW.job_id;
END;
CREATE TRIGGER job_download_growth_update AFTER UPDATE OF downloaded ON job_observations
WHEN NEW.downloaded>COALESCE(OLD.downloaded,0) AND OLD.first_download_growth_at IS NULL
BEGIN
 UPDATE job_observations SET first_download_growth_at=NEW.observed_at WHERE job_id=NEW.job_id;
END;

-- Observed timestamps only; NULL means the stage has not been observed.
-- The first positive downloaded counter is evidence of byte transfer, while
-- the downloader's "downloading" state alone is not.
CREATE VIEW dispatch_pipeline_times AS
SELECT j.job_id,a.attempt_id,a.instance_id,j.job_state,
 s.first_seen_at AS rss_received_at,t.created_at AS torrent_ready_at,
 a.started_at AS allocated_at,i.dispatch_started_at AS add_started_at,
 i.response_fact_at AS add_returned_at,
 COALESCE(a.confirmed_at,json_extract(p.evidence_json,'$.Now')) AS hash_observed_at,
 o.first_download_growth_at
FROM race_jobs j
LEFT JOIN rss_sources s ON j.source_id=s.site_id||'/'||s.source_key
LEFT JOIN job_torrent_artifacts t ON t.job_id=j.job_id
LEFT JOIN placement_attempts a ON a.job_id=j.job_id
LEFT JOIN submit_intents i ON i.attempt_id=a.attempt_id
LEFT JOIN pending_confirmations p ON p.attempt_id=a.attempt_id
LEFT JOIN job_observations o ON o.job_id=j.job_id;
