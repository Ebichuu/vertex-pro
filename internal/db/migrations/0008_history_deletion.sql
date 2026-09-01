-- 0008_history_deletion.sql
-- HIS-02：删除事实持久化。qB Torrent 被权威确认删除后，History 必须保留
-- 最后已知状态、累计上传量、分享率与删除发现时间；binding 行删除不得影响
-- 历史查询（设计 §2 产品定义与 §8.7.5 权威删除）。

CREATE TABLE history_deletion_facts (
    fact_id TEXT PRIMARY KEY,
    job_id TEXT NOT NULL REFERENCES race_jobs(job_id) ON DELETE RESTRICT,
    identity_key TEXT NOT NULL,
    qb_torrent_id TEXT NOT NULL,
    storage_target_id TEXT NOT NULL,
    deletion_source TEXT NOT NULL CHECK (deletion_source IN ('QB_USER_REMOVED', 'QUI_LIFECYCLE', 'UNKNOWN_ORIGIN')),
    deleted_at TEXT NOT NULL,
    last_known_observation TEXT NOT NULL CHECK (last_known_observation IN ('DOWNLOADING', 'SEEDING', 'COMPLETED')),
    last_uploaded_bytes INTEGER NOT NULL CHECK (last_uploaded_bytes >= 0),
    last_downloaded_bytes INTEGER NOT NULL CHECK (last_downloaded_bytes >= 0),
    last_ratio REAL CHECK (last_ratio IS NULL OR last_ratio >= 0),
    last_seen_snapshot_digest TEXT NOT NULL,
    last_seen_snapshot_revision INTEGER NOT NULL CHECK (last_seen_snapshot_revision >= 0),
    created_at TEXT NOT NULL,
    UNIQUE (job_id, qb_torrent_id)
);

CREATE INDEX history_deletion_facts_job_idx ON history_deletion_facts(job_id, deleted_at DESC);
