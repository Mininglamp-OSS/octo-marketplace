# Runtime plugin display-name reservations

Skill, Expert and Expert Team names are normalized by Unicode NFKC, trimming and case folding. Connectors retain their existing naming behavior.

Standalone, non-deleted resources reserve both editable and formally released names within their Space/type. Delisted resources retain those reservations. Runtime creation, rename, reupload, publication and approval also reject the effective released name of an active published public/system market resource across Spaces. Foreign private resources and unpublished global draft names are not exposed by that cross-Space lookup. A private incoming draft still cannot claim an existing market name.

The name and Expert Team migrations add nullable name keys, a scope-qualified unique index and reservation triggers. Historical NULL-key rows are checked transactionally during runtime writes; this PR does not backfill or rename old rows. Existing duplicate inventories must be reviewed before the separate historical tool in PR144 is applied.

The global-name migration additionally enforces a unique `(plugin_type, global_name_key)` for active, standalone, published public/system resources. The generated key prefers the published name over the editable draft and excludes Space from the namespace. This is the final concurrent-write guard even at `READ COMMITTED`; application locking reads alone do not protect the empty-name case at that isolation level. Private records, unpublished drafts, delisted/deleted/inactive rows and embedded resources do not occupy this global index. Existing NULL keys remain nullable for staged rollout. Any duplicate already populated global keys must be resolved before applying the migration; it never silently merges or renames records.

Conflicts return HTTP 409 DUPLICATE with details.conflict_reason=name_taken and rename guidance. Updates exclude their own resource ID. Published and editable names remain reserved independently; changing a draft does not free its release name.

This runtime PR excludes the database-wide name-backfill command and the legacy resource-conversion/rollback tools. Those follow in PR144 and do not gate deployment of the runtime endpoints.
