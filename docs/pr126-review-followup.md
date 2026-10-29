# PR126 review follow-up (historical)

> Historical evidence for PR #126 only. The stock-migration command and its
> exclusive packages described below have been removed. Do not use this record
> as an operating or rollback guide. Use [the current single-database migration
> guide](production-marketplace-migration.md), including its partial-commit
> recovery procedure and environment backup requirements.

Review target: `94de531`. This records the agent's code inspection and local
regressions. It does not replace the additional human review requested in
[the review](https://github.com/Mininglamp-OSS/octo-marketplace/pull/126#pullrequestreview-5647018971).

## Audit retention and schema upgrades

Stock migration audit schema is forward-only. Both the original audit migration
and the new access-index migration explicitly refuse `Down` with MySQL
SQLSTATE `45000`. A no-op Down would incorrectly remove sql-migrate's receipt
and allow a later Up to collide with retained tables. An explicit refusal keeps
both the audit data and receipt, and stops a full downgrade before any earlier
destructive migration can run on the upgraded schema.

At the time of PR #126, data rollback used the retired stock-migration command,
independently of schema Down. That command is no longer available; current
recovery follows the guide linked above. Isolated test databases are removed at database
cleanup. Historical reversible migrations keep real Up/Down tests on the
pre-audit schema; full current-schema upgrades and audit refusal have separate
tests. No test replaces or bypasses the production guard.

The new forward migration installs these indexes on existing and fresh schemas:

| Index columns | Access path |
| --- | --- |
| `plugin_install_attempts(space_id, plugin_id, created_at)` | Ops-only scoped resource/time inspection; application retry lookup uses the full existing PK |
| `plugin_stock_migration_batches(state, batch_id)` | Ops-only batch/state inspection; the application's joined scan may use the batch PK instead |
| `plugin_stock_migration_roots(batch_id, applied_at, root_plugin_id)` | Reverse apply order within a rollback batch, including the existing deterministic tie-break |

For example, an operator inspecting one resource's receipts must constrain both
`space_id` and `plugin_id` before filtering/ordering `created_at`. The index does
not introduce a cross-Space query or API. Index creation uses guarded online DDL
so a crash between DDL commits and the migration receipt can be replayed.
The first two indexes are operational support, not demonstrated application hot
paths; they add storage and insert/update maintenance cost and should be included
in capacity planning. The root ordering index matches the actual rollback query.

## Historical evidence scan evidence

`referenceindex.read` is a bounded complete-inventory read, not a paginated list.
It reads `limit+1` and refuses oversized evidence, preventing a partial inventory
from proving completeness. Adding a cursor without changing that proof would
be incorrect. Index metadata and unhinted `EXPLAIN FORMAT=JSON` on real MySQL 8
confirm the three existing scan orders:

| Query order | Selected index | Filesort |
| --- | --- | --- |
| `plugins ORDER BY plugin_id LIMIT 101` | `PRIMARY(plugin_id)` | false |
| `plugin_versions ORDER BY version_id LIMIT 101` | `PRIMARY(version_id)` | false |
| `plugin_version_releases ORDER BY version_id LIMIT 101` | `uq_plugin_release_version(version_id)` | false |

These fixture plans verify available ordering paths, not production scale or
latency. The backfill's existing 10,000-row evidence cap remains in force.

## Inspection of the three files named by the reviewer

- `internal/service/plugin/pinned_install.go`: traced all resolver waves, released
  root selection and retry pins, expert/member/skill identity and type checks,
  current connector reads, audience checks, artifact verification, stable skill
  names and both Fleet/legacy consumers. Repository installation reads enforce
  caller Space/ownership even for admins. Missing releases, wrong-resource pins,
  unsupported schemas and hidden targets fail before remote installation.
- `internal/backfill/stockmigration/plan.go`: traced root/embedded ownership,
  audience inheritance, missing or ambiguous evidence, historical pins, ordered
  member extraction, bounded labels and files, and credential quarantine. Preview
  excludes document bodies/configuration from its JSON mappings. MCP environment,
  headers, credential URLs and known secret flags with literal values are
  quarantined; exception reports use fixed reason codes.
- `internal/backfill/stockmigration/store.go`: traced batch/root identity, the G4
  gate, inventory re-read and preview digest, one-root transactional writes,
  immutable snapshot/release creation, audit insertion, interrupted replay and
  retirement. Rollback checks root and child version pointers, hashes, audience,
  lifecycle and deletion state under locks. Any later failure rolls back the
  transaction; extracted resources and historical versions are retained.

The earlier inspection missed the inline-MCP failure subsequently reproduced by
Octo-Q at `639d1f1`; it is not a continuing claim of no defects. The replacement
now refuses inline root/member capabilities and restores missing-root 404s.
File hydration now runs before inventory locks, followed by exact locked-source
and preview-digest checks. The full-inventory lock limit still applies and is
documented in the stock migration runbook. Existing tests
cover cross-Space/owner denial, release and artifact failures, current connector
drift, same-key retries, failed apply transaction rollback, root/child edit CAS,
audited resume, historical-byte retention and credential redaction. The requested
independent human review and hosted CI results remain separate acceptance steps.
