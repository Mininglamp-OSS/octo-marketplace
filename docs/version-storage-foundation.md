# Expert and skill version storage foundation

PR 1 of the version-management rollout adds durable saved history. Publication
and historical-version management use this foundation in PR 2; fixed expert/skill
dependencies and historical dependency protection follow in PR 3.

## Data semantics

`plugins.current_version_id` still identifies the latest saved snapshot.
`published_version_id` is nullable and reserved for formal publication; this PR
does not populate it or infer publication from listing state. The
`plugin_version_releases` table enforces snapshot ownership and unique release
labels per resource. A published pointer must reference a release of that same
resource. Existing snapshots and connector history remain untouched.

New expert, skill and expert-team saves append immutable `plugin_versions` rows.
The existing `version` field remains the save sequence; `declared_version` stores
the author's label. `snapshot_schema_version=1` identifies the stable snake_case
relation codec. Readers also accept legacy Go field names without rewriting old
JSON. Both review and version DTOs project that data onto their existing wire
schemas. New connector saves do not append versions; connector configuration
continues to use current-state reads and existing permissions/review.

`artifacts_json` inventories original files by relative path, size and SHA-256.
Inline bytes remain in the immutable package; external file bytes use host-private
object keys. Legacy `skill/ref.json` pointers in the resource's own Space are also
frozen, including the real SKILL.md and archive bytes. The inventory identifies
those referenced objects under `skill/ref.json/<pointer-field>`. Legacy roots or
missing objects cannot be converted by guessing: the existing expand-skills
migration/exception workflow must handle them. No historical data migration is
run automatically.

The internal `GetSavedVersion` repository method returns complete saved content
only to the owner in the same Space, or an explicitly authorized admin scope. It
is not a public release-read endpoint. Object keys, file inventory and draft
content do not enter public metadata DTOs.

## File lifetime and integrity

Before committing a new save/review, the service reads and validates external
files and freezes them at `plugins/<space>/attachments/versions/<full-sha256>`.
Identical bytes can share an object within that Space. Local storage creates an
absent object atomically using a hard link; S3-compatible storage uses conditional
PUT with `If-None-Match: *`. Existing objects are read and checked rather than
overwritten. Unsupported conditional writes fail closed.

Generic upload, copy and delete operations cannot modify that reserved namespace.
Changing/removing a file, publishing another version, soft-deleting a resource,
or cancelling a review therefore leaves earlier bytes intact. Historical files
have no automatic count/time-based eviction in this rollout.

An upload failure prevents the database save. A database failure can leave an
unreferenced immutable object. It is intentionally retained because a concurrent
save may already refer to the same bytes; failure compensation only removes
ordinary staging objects. There is no new physical garbage collector. This also
protects shared objects during concurrent retries and abandoned reviews.

Object-store operations happen before the database transaction. Snapshot content,
file inventory and the saved-version pointer are committed in one transaction;
a failure cannot expose a partial saved version. Immutability here is enforced
through the application's storage APIs, not an object-lock policy against an
operator with direct bucket/filesystem access. Recorded digests detect corruption
when bytes are validated. Files lost before this rollout are not recoverable
merely because a historical JSON reference survives.

## Verification and deployment

Run the relevant Go package tests, then the full suite with an isolated MySQL
`TEST_MYSQL_DSN`; migration tests create/drop their own temporary databases. Tests
cover migration Up/Down, release ownership, tenant isolation, retained old bytes,
failed/missing/corrupt uploads, concurrency, and both storage adapters. OpenAPI
check/diff verify unchanged wire contracts.

The incremental schema is deployed first. Old snapshots keep NULL foundation
metadata; they are not relabelled as formal releases. Publication/read switching
and dependency pinning are separate rollout steps. Application rollback must
retain new snapshots and objects; the Down migration is for isolated verification,
not a production rollback procedure. Only a compatibility build that understands both codecs may resume writes or
approvals after the new codec is enabled; an arbitrary older binary is not a
validated application rollback target.
