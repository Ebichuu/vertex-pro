-- Preserve the exact RSS torrent bytes with the immutable job decision.
-- Never store the source URL or credentials in artifact metadata.
CREATE TABLE job_torrent_artifacts (
    job_id TEXT PRIMARY KEY REFERENCES race_jobs(job_id) ON DELETE CASCADE,
    body BLOB NOT NULL CHECK(length(body) > 0 AND length(body) <= 16777216),
    sha256 TEXT NOT NULL CHECK(length(sha256) = 64),
    created_at TEXT NOT NULL
);
