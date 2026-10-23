-- Existing dedupe rows remain historical baselines; never replay them on upgrade.
ALTER TABLE rss_sources ADD COLUMN legacy_seen INTEGER NOT NULL DEFAULT 1 CHECK (legacy_seen IN (0,1));

CREATE TABLE rss_source_consumers (
    site_id TEXT NOT NULL,
    feed_id TEXT NOT NULL REFERENCES feeds(feed_id) ON DELETE CASCADE,
    source_key TEXT NOT NULL,
    source_json TEXT NOT NULL CHECK (json_valid(source_json)),
    state TEXT NOT NULL CHECK (state IN ('PENDING','DONE','SKIPPED','FAILED')),
    attempts INTEGER NOT NULL DEFAULT 0 CHECK (attempts BETWEEN 0 AND 5),
    next_attempt_at TEXT NOT NULL,
    deadline_at TEXT NOT NULL,
    last_error_code TEXT NOT NULL DEFAULT '',
    PRIMARY KEY (site_id,feed_id,source_key),
    FOREIGN KEY (site_id,source_key) REFERENCES rss_sources(site_id,source_key) ON DELETE CASCADE
);
CREATE INDEX rss_source_consumers_due ON rss_source_consumers(feed_id,state,next_attempt_at);
