-- +migrate Up
-- A15 review observations. Both columns are audit evidence for the review
-- window, not version pins: submit resolves the frozen graph's latest formal
-- releases into them and approve re-resolves to detect drift. NULL means the
-- request predates observations (legacy rows are approved without the check);
-- an empty array means the graph genuinely had no dependency of that kind.
ALTER TABLE plugin_review_requests
 ADD COLUMN resolved_dependency_observations_json JSON NULL,
 ADD COLUMN connector_observations_json JSON NULL,
 ADD CONSTRAINT chk_review_resolved_observations_array CHECK (
   resolved_dependency_observations_json IS NULL
   OR JSON_TYPE(resolved_dependency_observations_json) = 'ARRAY'),
 ADD CONSTRAINT chk_review_connector_observations_array CHECK (
   connector_observations_json IS NULL
   OR JSON_TYPE(connector_observations_json) = 'ARRAY');

-- +migrate Down
-- Isolated verification only; production rollback retains additive schema.
ALTER TABLE plugin_review_requests
 DROP CONSTRAINT chk_review_connector_observations_array,
 DROP CONSTRAINT chk_review_resolved_observations_array,
 DROP COLUMN connector_observations_json,
 DROP COLUMN resolved_dependency_observations_json;
