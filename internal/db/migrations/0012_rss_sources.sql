-- RSS 来源去重持久化：rsssource.Store 的生产实现存储于本表。
-- fingerprint 用于识别同键条目的字段变化（标题/大小更新）。
CREATE TABLE rss_sources (
    site_id TEXT NOT NULL REFERENCES sites(site_id) ON DELETE CASCADE,
    source_key TEXT NOT NULL,
    guid TEXT NOT NULL,
    url TEXT NOT NULL,
    title TEXT NOT NULL,
    size_bytes INTEGER,
    fingerprint TEXT NOT NULL,
    first_seen_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    PRIMARY KEY (site_id, source_key)
);
CREATE INDEX idx_rss_sources_site_first_seen ON rss_sources(site_id, first_seen_at);
