-- +migrate Up
-- Editing concurrency is independent of immutable content/version identity.
ALTER TABLE plugins ADD COLUMN edit_revision BIGINT UNSIGNED NOT NULL DEFAULT 1,
  ADD CONSTRAINT chk_plugin_edit_revision CHECK (edit_revision > 0);

-- +migrate Down
-- Isolated verification only; production rollback retains additive schema.
ALTER TABLE plugins DROP CONSTRAINT chk_plugin_edit_revision,
  DROP COLUMN edit_revision;
