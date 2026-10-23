-- UI-11: validated single-row settings. Runtime workers read these on startup.
CREATE TABLE app_settings (
    id INTEGER PRIMARY KEY CHECK (id = 1),
    timezone TEXT NOT NULL DEFAULT 'UTC' CHECK (length(trim(timezone)) BETWEEN 1 AND 100),
    rss_poll_interval_minutes INTEGER NOT NULL DEFAULT 60 CHECK (rss_poll_interval_minutes BETWEEN 1 AND 1440),
    rate_limit_per_minute INTEGER NOT NULL DEFAULT 30 CHECK (rate_limit_per_minute BETWEEN 1 AND 600),
    max_concurrent_rss_fetches INTEGER NOT NULL DEFAULT 4 CHECK (max_concurrent_rss_fetches BETWEEN 1 AND 32),
    session_ttl_hours INTEGER NOT NULL DEFAULT 24 CHECK (session_ttl_hours BETWEEN 1 AND 720),
    stats_raw_retention_days INTEGER NOT NULL DEFAULT 30 CHECK (stats_raw_retention_days BETWEEN 1 AND 3650),
    stats_hourly_retention_days INTEGER NOT NULL DEFAULT 180 CHECK (stats_hourly_retention_days BETWEEN stats_raw_retention_days AND 3650),
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    updated_at TEXT NOT NULL
);
INSERT INTO app_settings(id, updated_at) VALUES (1, strftime('%Y-%m-%dT%H:%M:%fZ', 'now'));
