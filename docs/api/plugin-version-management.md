# Expert and skill version management (V2 local rollout)

Enable `PLUGIN_VERSION_MANAGEMENT_ENABLED=true` in an isolated environment built
with the V1 version/artifact foundation migrations and the restore-source
migration. The default is off while dependency pins/protection (R1-R4), the
inventory (M1) and the adoption case below are still being rolled out. Connectors
keep their existing current-state behavior and review flow.

## Publication and editing

Saving a skill, expert or expert team advances its saved snapshot only. Private
publication and marketplace-admin publication create a formal release and move
`published_version_id` atomically. Space-visible publication follows the existing
automatic/manual review policy. Approval publishes the frozen submission and
preserves any later draft and draft relations; rejection leaves the old release.
A **first listing** also applies the frozen edge set to the live graph and
promotes the container's embedded skills/member experts, so another Space member
can see and install them. A managed upgrade leaves the live draft graph alone
because the release row carries the frozen topology the market reads.

Default tenant detail, catalog content/search/tags, graph and skill artifact reads
use released content when a formal release pointer exists. Editing uses the new
explicit draft endpoint or the existing gated admin detail endpoint. In public
content projections, existing `current_version_id`/`current_version` fields identify
the served snapshot; database `plugins.current_version_id` still identifies the
latest saved draft. History's `is_current` and `is_published` expose the distinction
without returning unpublished content to other readers.

Existing records with no formal release retain legacy read compatibility. A published resource must have a verified release baseline before saving a
new draft. Re-publishing a listed resource that carries complete V1 snapshot
metadata but no release registers that approved snapshot as the baseline: the
snapshot must still match the live row (hashes and file inventory), and a
Space-visible resource must also have an approved review request over the same
version and content. Private resources have no review channel, so the matching
snapshot is the evidence. Older records lacking the necessary metadata/evidence
return `409` with `reason=version_migration_required` and wait for the separately
planned data inventory/migration; historical rows are not rewritten or all marked
as published. Published container ZIP rebuilds are refused
in this staged mode pending PR3; the compatibility mode remains available with
the rollout off. Disabling this staged switch is not a production rollback strategy: the legacy read path expects current content, so do not turn it off against a database containing unpublished managed drafts. Production switching/rollback remains gated on PR3 and the migration plan. This work does not implement fixed-version Fleet installation.

## Version endpoints

All paths below are relative to `/api/v1`, require existing authentication, and
use the repository's standard `{data: ...}` / `{error: ...}` envelopes. File
successes are the explicit byte-stream exception (R1/R13); errors remain JSON.
Resource/path naming follows R6/R7 and operation IDs follow R10.

| Capability | Tenant path | Admin path |
| --- | --- | --- |
| Current draft | `GET /plugins/{plugin_id}/draft` | Existing `GET /admin/plugins/{plugin_id}` |
| Version list | `GET /plugins/versions?plugin_id=...` | `GET /admin/plugins/{plugin_id}/versions` |
| Version detail | `GET /plugins/{plugin_id}/versions/{version_id}` | Same suffix under `/admin/plugins` |
| Original file | `GET /plugins/{plugin_id}/versions/{version_id}/file?path=AGENTS.md` | Same suffix under `/admin/plugins` |
| Restore as new draft | `POST /plugins/{plugin_id}/versions/{version_id}/restore` | Same suffix under `/admin/plugins` |

Lists use `page` and `page_size` (20 default, 100 maximum) and offset pagination
(R5). Authors in their resource's Space and gated marketplace admins may read
saved drafts. Other currently authorized users can read formal releases only.
A version must belong to the requested resource; resource deletion or revoked
current access also prevents historical reads. Hidden dependency graphs fail
closed rather than returning a misleading partial graph.

Version metadata adds `declared_version`, optional `release` (label, actor/time),
`is_current`, `is_published`, and optional `restored_from_version_id`.
Version detail contains `{version, manifest_json, plugin_json}`. Host storage
sidecars/object keys are excluded, including locations inside legacy
`skill/ref.json` metadata. The original bytes remain internal and immutable.
The historical file API verifies stored size/digest and refuses missing/corrupt
objects, traversal, or cross-Space references. Legacy pointer objects can be
retrieved using their inventory paths (`skill/ref.json/object_key`,
`skill/ref.json/zip_object_key`, `skill/ref.json/file_url`), or the aliases
`SKILL.md` and `skill/package.zip`; the private descriptor itself is not downloadable.

Restore body: `{"version":"3.0.0"}`. The label must be valid and strictly later
than the current saved label. Restore copies historical content and relations
to a new saved version, preserves current access settings and records provenance.
It never moves the published pointer; publish/review again to release it. A stale
head or conflicting release label is rejected. Historical files/versions have no
physical deletion or automatic retention expiry in this rollout.

Existing tenant `POST /plugins/publish` publishes the current private draft or
submits the current Space draft for review. For the admin surface,
`POST /admin/plugins/{plugin_id}/publish` explicitly publishes a saved draft.

Admin writes follow the confirmed state matrix: creating or importing a system
resource, and saving/uploading a **listed** system resource, publish in the same
transaction (current and published are the same snapshot). Saving a delisted or
draft system resource, or any tenant resource, only advances the editable head
and leaves the published pointer and listing state unchanged. Admin DTOs do not
expose the published pointer.

## Local acceptance

The prepared PR2 API uses only its own demo database and storage. The test service
has development authentication; use it only on the loopback port mapping. The
script creates one private test expert per run and checks published v1, saved v2,
historical file bytes and restoring v1 as a distinct v3 draft:

```bash
python3 scripts/test-version-management.py --base-url http://127.0.0.1:32772
```

The script prints the resource and version IDs for subsequent manual requests.
Current sandbox identity is `user-1` in `space-a`; no production records are used.
Web/Admin UI is outside this backend change.
