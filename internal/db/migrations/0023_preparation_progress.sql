-- Durable clocks distinguish claimed preparation from retry backoff, and a
-- sustained unavailable target from a brief snapshot publication change.
ALTER TABLE rss_source_consumers ADD COLUMN preparation_started_at TEXT;
ALTER TABLE dispatch_queue ADD COLUMN unavailable_since TEXT;
ALTER TABLE dispatch_queue ADD COLUMN evidence_retries INTEGER NOT NULL DEFAULT 0 CHECK(evidence_retries>=0);
