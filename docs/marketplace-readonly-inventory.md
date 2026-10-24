# M1 read-only inventory

Run without a database from the repository root:

```sh
go run ./cmd/marketplace-inventory --fixture internal/inventory/testdata/legacy.json > /tmp/marketplace-inventory.json
go test ./internal/inventory ./cmd/marketplace-inventory
```

The fixture contains three fictitious resources: an expert with an embedded skill, and another author's same-name skill. The command imports JSON into memory, analyzes it without mutation, and outputs a deterministic JSON report. The fixture path never opens a database, even if a DSN is present.

The report contains resource IDs, owners, scopes, parent relationships, version/release counts, same-name candidate IDs, and fixed diagnostic codes. A listed legacy resource without a release is `listed_publication_unknown`, not automatically published. Missing pins and missing attachment metadata are reported, never replaced with current values. Same-name groups do not authorize reuse. No placement inference is made.

Database mode requires V1 schema already installed and an explicitly authorized SELECT-only account:

```sh
# Supply MARKETPLACE_INVENTORY_DSN through the environment, not command arguments.
go run ./cmd/marketplace-inventory --all-spaces --max-rows 100000 --timeout 2m
```

It uses four SELECT queries inside a repeatable-read read-only transaction, ending with ROLLBACK. It never runs application startup or migrations. The row limit is per table; overflow/error produces no report. DSNs with session parameters, multiStatements or allowAllFiles are rejected. This command is an operator tool, not an authenticated tenant endpoint; scope includes all rows in the selected Marketplace database, including deleted resources.

This iteration validates fixed fixtures only. Do not point it at production or real tenant data. A separate authorized inventory session must assess actual rollout readiness.

`object_verification: not_checked` is deliberate: this tool checks metadata only. `storage_object_not_checked` does not mean an object exists or its digest is correct. Report files contain operator metadata and should not be published. Package contents, names, storage keys, paths and credential values are excluded; this is not a secret scanner.

`distribution` is a sparse cross-tab of visibility × listing × identity × deleted,
including every input resource exactly once. Visibility buckets are `private`,
`space`, `system`, `legacy_public`, and `unknown`; listing buckets are `draft`,
`published`, `delisted`, and `unknown`. Identity is `embedded` or `top_level`.
Legacy `public` is reported explicitly rather than silently converted to system.
Unknown values are not guessed. Deleted rows have their own cells. Listing is
observed metadata, not proof of a formal release. Missing combinations have zero
rows rather than fabricated entries; an empty input emits `distribution: []`.

The isolated SQL fixture and commands are documented in
[tools/integration-harness/README.md](../tools/integration-harness/README.md).
Tool/sample acceptance does not authorize target-environment inventory or satisfy
actual rollout readiness. M3 may consume evidence and exception codes, but this
tool never creates references, releases, resources, or backfill rows.
