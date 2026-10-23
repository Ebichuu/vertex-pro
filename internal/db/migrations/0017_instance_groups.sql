CREATE TABLE instance_groups (
 group_id TEXT PRIMARY KEY,
 name TEXT NOT NULL,
 members_json TEXT NOT NULL CHECK(json_valid(members_json)),
 revision INTEGER NOT NULL DEFAULT 1 CHECK(revision>0),
 updated_at TEXT NOT NULL
);
