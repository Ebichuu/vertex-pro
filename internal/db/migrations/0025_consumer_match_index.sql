-- feed_runtime_state.last_matched counts matched consumers discovered since the
-- feed's last poll. The count runs on every finished item, so it must touch only
-- matched rows in that window rather than the feed's whole consumer history.
CREATE INDEX ix_consumer_matched_discovered ON rss_source_consumers(feed_id, discovered_at) WHERE matched = 1;
