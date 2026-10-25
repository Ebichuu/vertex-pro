-- Deferred permission waits do not own workers or spend processing retries.
-- A monotonically increasing claim generation fences callbacks even when a
-- yielded attempt returns its retry budget and is claimed again.
ALTER TABLE rss_source_consumers ADD COLUMN claim_generation INTEGER NOT NULL DEFAULT 0 CHECK(claim_generation>=0);
ALTER TABLE rss_source_consumers ADD COLUMN artifact_deferred_until TEXT;
ALTER TABLE rss_source_consumers ADD COLUMN artifact_resume_url TEXT NOT NULL DEFAULT '';
ALTER TABLE rss_source_consumers ADD COLUMN artifact_redirects INTEGER NOT NULL DEFAULT 0 CHECK(artifact_redirects BETWEEN 0 AND 9);
CREATE INDEX ix_consumer_artifact_deferred ON rss_source_consumers(artifact_deferred_until) WHERE state='PENDING' AND artifact_deferred_until IS NOT NULL;
