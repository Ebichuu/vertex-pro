-- Preserve historical references when resources are removed from configuration.
ALTER TABLE sites ADD COLUMN credential_ref TEXT NOT NULL DEFAULT '';
ALTER TABLE sites ADD COLUMN archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1));
ALTER TABLE feeds ADD COLUMN archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1));
ALTER TABLE rules ADD COLUMN archived INTEGER NOT NULL DEFAULT 0 CHECK(archived IN (0,1));
