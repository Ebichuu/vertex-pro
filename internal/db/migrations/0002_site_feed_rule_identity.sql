CREATE TABLE sites (
    site_id TEXT PRIMARY KEY,
    name TEXT NOT NULL,
    base_url TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (base_url)
);

CREATE TABLE feeds (
    feed_id TEXT PRIMARY KEY,
    site_id TEXT NOT NULL REFERENCES sites(site_id) ON DELETE CASCADE,
    name TEXT NOT NULL,
    feed_url TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    etag TEXT,
    last_modified TEXT,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (site_id, feed_url)
);

CREATE TABLE identities (
    identity_id TEXT PRIMARY KEY,
    canonical_hash TEXT NOT NULL UNIQUE,
    title TEXT NOT NULL,
    size_bytes INTEGER CHECK (size_bytes IS NULL OR size_bytes >= 0),
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL
);

CREATE TABLE identity_sources (
    identity_id TEXT NOT NULL REFERENCES identities(identity_id) ON DELETE CASCADE,
    site_id TEXT NOT NULL REFERENCES sites(site_id) ON DELETE CASCADE,
    source_key TEXT NOT NULL,
    source_url TEXT,
    created_at TEXT NOT NULL,
    PRIMARY KEY (site_id, source_key)
);

CREATE TABLE rules (
    rule_id TEXT PRIMARY KEY,
    site_id TEXT REFERENCES sites(site_id) ON DELETE SET NULL,
    name TEXT NOT NULL,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    priority INTEGER NOT NULL DEFAULT 0,
    revision INTEGER NOT NULL DEFAULT 1 CHECK (revision > 0),
    state TEXT NOT NULL DEFAULT 'PENDING' CHECK (state IN ('PENDING', 'MATCHED', 'REJECTED', 'RUNNING')),
    definition_json TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    UNIQUE (site_id, name)
);

CREATE INDEX feeds_site_id_idx ON feeds(site_id);
CREATE INDEX identity_sources_identity_id_idx ON identity_sources(identity_id);
CREATE INDEX rules_site_id_priority_idx ON rules(site_id, priority DESC, revision DESC, rule_id ASC);