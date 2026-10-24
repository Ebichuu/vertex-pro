ALTER TABLE app_settings ADD COLUMN request_burst INTEGER NOT NULL DEFAULT 4 CHECK(request_burst BETWEEN 1 AND 16);
ALTER TABLE app_settings ADD COLUMN max_concurrent_preparations INTEGER NOT NULL DEFAULT 8 CHECK(max_concurrent_preparations BETWEEN 1 AND 32);
ALTER TABLE app_settings ADD COLUMN max_preparations_per_host INTEGER NOT NULL DEFAULT 2 CHECK(max_preparations_per_host BETWEEN 1 AND 8);
ALTER TABLE rss_source_consumers ADD COLUMN rate_wait_started_at TEXT;
ALTER TABLE rss_source_consumers ADD COLUMN rate_wait_ms INTEGER NOT NULL DEFAULT 0 CHECK(rate_wait_ms >= 0);
