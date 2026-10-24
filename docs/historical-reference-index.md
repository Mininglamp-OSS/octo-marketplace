# Historical reference index maintenance

This tool reconstructs only exact historical reference facts. It does not extract embedded resources, publish versions, change visibility, repair files or enable features. An index row is not an authorization grant.

## Preview

Offline JSON contains `resources` (`id`, `type`, `embedded`), `snapshots` (`plugin_id`, `version_id`, `relations`) and `releases` (`plugin_id`, `version_id`). It must contain the complete evidence set; target versions cannot be omitted and replaced with current versions.

```sh
go run ./cmd/marketplace-reference-index --fixture evidence.json
```

Database reads require separate operator authorization and the DSN in `MARKETPLACE_REFERENCE_INDEX_DSN` (never a command argument). Use a SELECT-only account for preview:

```sh
go run ./cmd/marketplace-reference-index --all-spaces
```

Reads use a repeatable-read, read-only transaction and roll back. The default/max limit is 10,000 rows per evidence table; excess is an error, never a truncated success. There are no network requests or package/configuration contents in the report. Current visibility is not used to invent historical audience facts.

## Apply one verified snapshot

Requires the R index migration and a separately authorized database session with index INSERT permission. Supply the immutable source `version_id` and `snapshot_digest` from preview:

```sh
go run ./cmd/marketplace-reference-index --all-spaces --apply \
  --version-id SOURCE_VERSION --expected-digest PREVIEW_DIGEST
```

The tool re-reads and locks bounded evidence, validates every relation in the selected snapshot, checks all existing index facts, and inserts missing facts atomically. Same facts are a no-op; stale evidence, unknown pins, wrong ownership/type, missing releases, embedded resources or conflicting index facts stop the transaction. Completed versions can be safely repeated after interruption. A failed or incomplete snapshot never receives a partially reconstructed index from this tool.

This small-batch maintenance implementation deliberately holds shared evidence locks while indexing one snapshot. It is for isolated rehearsal and bounded batches; larger installations require a reviewed paginated evidence strategy before use. No production invocation is part of development or startup.

## Readiness and rollback

`complete: false` blocks coverage claims for that version. Even `complete: true` proves snapshot/index facts only; runtime access/audience checks remain mandatory. Before enablement, reconcile snapshot IDs against the report and separately verify current policy and artifact availability.

Rollback stops index use and retains facts. Never delete version history, fabricate releases or remove extracted/shared resources as an index rollback. Disputed full-resource migration remains outside this tool.
