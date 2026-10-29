# Runtime plugin display-name reservations

Skill, Expert and Expert Team names are normalized by Unicode NFKC, trimming and case folding. Connectors retain their existing naming behavior.

Standalone, non-deleted resources reserve both editable and formally released names within their Space/type. Delisted resources retain those reservations. Runtime creation, rename, reupload, publication and approval also reject the effective released name of an active published public/system market resource across Spaces. Foreign private resources and unpublished global draft names are not exposed by that cross-Space lookup. A private incoming draft still cannot claim an existing market name.

The name and Expert Team migrations add nullable name keys, a scope-qualified unique index and reservation triggers. Historical NULL-key rows are checked transactionally during runtime writes; this PR does not backfill or rename old rows. Existing duplicate inventories must be reviewed before the separate historical tool in PR144 is applied.

Conflicts return HTTP 409 DUPLICATE with details.conflict_reason=name_taken and rename guidance. Updates exclude their own resource ID. Published and editable names remain reserved independently; changing a draft does not free its release name.

This runtime PR excludes the database-wide name-backfill command and the legacy resource-conversion/rollback tools. Those follow in PR144 and do not gate deployment of the runtime endpoints.
