-- +migrate Up
-- ============================================================================
-- mcp-icon-object-storage :: mcp_servers.icon_version
-- ============================================================================
-- Icons move off inline base64 `data:` URLs onto an S3-compatible object
-- store (see internal/blob). After this migration the `icon` column stores a
-- storage URL/key produced by POST /api/v1/mcps/{id}/icon, not base64.
--
--   * `icon_version` is a monotonically increasing counter bumped on every
--     successful upload. It lets clients bust their image cache without a new
--     URL (append ?v=<icon_version>) and gives the storage key a stable,
--     collision-free suffix: mcp_icon/{partition}/{mcp_id}/{version}.png.
--   * We intentionally KEEP `icon` as MEDIUMTEXT. Existing rows may still hold
--     large base64 `data:` URLs; the product decision is to leave that legacy
--     content readable (the frontend `isImageIcon` renders both base64 and
--     URLs), so we do NOT shrink the column and risk truncating stored data.
--     New writes store short URLs that fit comfortably.
--   * No backfill: legacy base64 rows keep icon_version = 0 and their inline
--     data URL until the owner re-uploads through the new endpoint.
-- ============================================================================

-- +migrate StatementBegin
ALTER TABLE `mcp_servers`
  ADD COLUMN `icon_version` INT NOT NULL DEFAULT 0
  COMMENT 'icon object-storage version; bumped per upload, used for cache-busting and the storage key suffix'
  AFTER `icon`;
-- +migrate StatementEnd

-- +migrate Down
ALTER TABLE `mcp_servers` DROP COLUMN `icon_version`;
