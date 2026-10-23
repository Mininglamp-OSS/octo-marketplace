# M1 read-only inventory

Goal: run operator metadata inventory before any version reader switch, with a reproducible offline fixture path. This is not a tenant API or a migration.

Load-bearing behavior:
- Database reads use a repeatable-read, read-only transaction, SELECT only, and rollback on success/failure. No app bootstrap/migrations.
- SQL NULL, JSON null and empty JSON collections retain their distinct meanings; unknown history is not fabricated.
- Output excludes names, content, paths, storage keys and credentials. Operator-only all-space metadata requires explicit acknowledgment.
- Per-table limits fail closed without producing partial reports. Publication requires release evidence; listed status alone is not evidence.

Out of scope: real tenant data, production access, migration writes, secret scanning, object-store GET/HEAD or digest verification, product decisions G2–G5, feature enablement.

Acceptance: offline import/read/report end-to-end is deterministic and input unchanged; NULL/JSON SQL cases, rollback, query failure redaction and row limits tested; isolated fixture MySQL supports SELECT and refuses mutations. Document object verification as not_checked.
