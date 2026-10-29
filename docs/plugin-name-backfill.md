# Historical plugin-name backfill

Depends on the runtime name columns and reservation triggers from the Web runtime companion PR. This operator-only tool reports and populates existing keys; it does not change resource visibility, create publications, rename records or convert resources.

## Inventory rules

- Within one Space/type, both draft and published names reserve their normalized keys. Soft-deleted and embedded records are excluded.
- Across Spaces, compare only effective released names of active published public/system records. An existing private or unreleased record may predate a later global listing; inventory repair does not require that unrelated historical name to change.
- This inventory rule is distinct from a new runtime write: new uploads, renames and extracted standalone migration resources still check the market names that already exist at write time.
- Cross-Space public collisions are reported with scope `global-market`; same-Space groups retain their Space scope. Invalid names block apply.

## Operation

Inject MYSQL_DSN securely in the environment, then run:

```sh
go run ./cmd/plugin-name-backfill --all-spaces
go run ./cmd/plugin-name-backfill --all-spaces --apply
```

Review duplicate groups first. The tool never silently merges or renames records. Apply uses a repeatable-read transaction, locks and reanalyzes the full inventory, checks all participation/publication metadata against the initial report, then writes both keys. New or changed rows cause a retry rather than stale writes. Run in an authorized maintenance window; this is database-wide locking, not a paged online job.

`applied:true` means commit completed. A blocked run reports false; a transaction failure does not emit a success report. Existing derived keys are computed with the same Unicode normalization as runtime writes. This PR does not include PR159 or replace the resource-conversion architecture.
