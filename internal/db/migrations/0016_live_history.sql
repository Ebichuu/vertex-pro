CREATE TABLE job_observations (
 job_id TEXT PRIMARY KEY REFERENCES race_jobs(job_id) ON DELETE CASCADE,
 instance_id TEXT NOT NULL,
 qb_torrent_id TEXT NOT NULL,
 title TEXT NOT NULL DEFAULT '',
 state TEXT NOT NULL DEFAULT '',
 downloaded INTEGER CHECK(downloaded IS NULL OR downloaded>=0),
 uploaded INTEGER CHECK(uploaded IS NULL OR uploaded>=0),
 ratio REAL,
 completed_at TEXT NOT NULL DEFAULT '',
 observed_at TEXT NOT NULL,
 snapshot_generation INTEGER NOT NULL,
 snapshot_revision INTEGER NOT NULL,
 snapshot_digest TEXT NOT NULL,
 missing_count INTEGER NOT NULL DEFAULT 0,
 deleted_at TEXT NOT NULL DEFAULT ''
);

CREATE TABLE job_observation_events (
 id INTEGER PRIMARY KEY AUTOINCREMENT,
 job_id TEXT NOT NULL REFERENCES race_jobs(job_id),
 state TEXT NOT NULL,
 observed_at TEXT NOT NULL,
 snapshot_generation INTEGER NOT NULL,
 snapshot_revision INTEGER NOT NULL,
 snapshot_digest TEXT NOT NULL,
 UNIQUE(job_id,snapshot_generation,snapshot_revision,state)
);
