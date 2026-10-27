# Plugin display-name uniqueness

## Semantics

Marketplace display names are unique per Space and per Plugin type:

```text
(space_id, plugin_type, normalized_name)
```

- Only standalone (non-embedded) Expert/Skill rows participate. Embedded legacy
  children are not market-display rows; their extracted standalone replacements
  participate under the same name.
- Expert and Skill names are checked independently, so an Expert and a Skill may
  share a name.
- The same name may exist in different Spaces.
- Soft-deleted rows (`deleted_at IS NOT NULL`) do not reserve a name.
- Delisted rows are not deleted and continue to reserve their name.
- Names are normalized with Unicode NFKC, leading/trailing whitespace trimming,
  and Unicode case folding. Internal whitespace is preserved. The implementation
  lives in `internal/model/plugin_name.go`; MySQL LOWER/TRIM is not equivalent.

## Storage

`plugins.name_key` stores the normalized key. `plugins.name_scope` stores the
Space key (`''` for a system row). `plugins.active_name_key` is a generated column
that is non-NULL only for active, non-embedded Expert/Skill rows. The unique index is:

```text
uq_plugins_active_name (name_scope, plugin_type, active_name_key)
```

The index is race-safe. Application validation provides the friendly error; the
index is the final authority.

Duplicate creation/update returns HTTP 409 `DUPLICATE` with
`details.conflict_reason=name_taken` and `details.field=plugin_name`.

## Existing data

Apply schema migration `20261027-00-plugin-name-uniqueness.sql` first. Historical
rows stay outside the index (`name_key=NULL`) until explicitly backfilled.

Run the read-only report first:

```bash
MYSQL_DSN='...' go run ./cmd/plugin-name-backfill --all-spaces
```

Resolve every reported duplicate group without automatic suffixing:

- same content: choose one canonical row and reuse/merge it;
- different content: owner renames one row;
- delisted: still occupies the name;
- soft-deleted: does not occupy the name.

Then apply the backfill:

```bash
MYSQL_DSN='...' go run ./cmd/plugin-name-backfill --all-spaces --apply
```

The command refuses to apply while duplicate groups or invalid names remain. Do
not delete rows or disable the unique index to force it through.

## Migration

The stock-migration tool blocks an extracted Expert/Skill whose normalized name
is already occupied by another active plugin in the same Space. It never appends
an installation suffix or silently renames a market display name. An operator
must rename/reuse the source and re-run preview before applying that root.
