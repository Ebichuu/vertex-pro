-- 0009_feed_poll_interval.sql
-- Adds the scheduler-owned polling cadence without rewriting the published 0002 schema.
ALTER TABLE feeds ADD COLUMN poll_minutes INTEGER NOT NULL DEFAULT 15 CHECK (poll_minutes BETWEEN 1 AND 1440);
