CREATE TABLE installation_identity (id INTEGER PRIMARY KEY CHECK(id=1), token TEXT NOT NULL);
INSERT INTO installation_identity(id,token) VALUES(1,lower(hex(randomblob(16))));
CREATE TABLE qui_policy_bindings (
 instance_id TEXT NOT NULL,
 rule_id TEXT NOT NULL REFERENCES rules(rule_id),
 rule_revision INTEGER NOT NULL,
 qui_id INTEGER NOT NULL CHECK(qui_id>0),
 tag TEXT NOT NULL,
 payload_json TEXT NOT NULL CHECK(json_valid(payload_json)),
 enabled INTEGER NOT NULL CHECK(enabled IN (0,1)),
 last_error_code TEXT NOT NULL DEFAULT '',
 updated_at TEXT NOT NULL,
 PRIMARY KEY(instance_id,rule_id,rule_revision)
);
