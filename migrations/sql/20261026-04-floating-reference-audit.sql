-- +migrate Up
-- A version on a relation is optional audit evidence, not a selection pin.
ALTER TABLE plugin_version_relation_refs DROP CHECK chk_plugin_version_ref_type,
 ADD CONSTRAINT chk_plugin_version_ref_type CHECK (
 relation_type IN ('expert_skill','expert_team_expert')
 OR (relation_type='expert_connector' AND target_version_id IS NULL));

-- +migrate Down
-- Optional-audit history cannot be converted back into required pins safely.
SIGNAL SQLSTATE '45000' SET MESSAGE_TEXT = 'Floating reference audit downgrade refused';
