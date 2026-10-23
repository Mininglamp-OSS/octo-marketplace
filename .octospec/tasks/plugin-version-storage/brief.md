# Immutable plugin version and artifact foundation (PR 1 of 3)

## Goal
Preserve complete expert/skill history in database snapshots and immutable stored
bytes, and add publication metadata for the next PR without switching public reads
or publication behavior. Expert teams reuse the same foundation. Connectors remain
current-state resources; existing connector history is retained.

## Load-bearing behavior
- Version snapshots are append-only, with stable snake_case relation encoding and
  backwards-compatible decoding of existing Go field names.
- Database metadata distinguishes saved versions from future formal releases;
  no historical save is assumed published.
- Stored files are retained across versions; immutable attachment writes cannot
  overwrite an existing object, and reused data is checked for size/digest.
- Caller-referenced mutable upload objects must be frozen before a version is saved.
- Cleanup must not delete objects referenced by historical snapshots/reviews.

## Out of scope
New HTTP endpoints, UI, publication/read switching, dependency pins/indexes,
legacy package conversion, production migration/cleanup, and Fleet changes.

## Acceptance
1. Incremental migration works up/down in isolated MySQL; nullable new metadata
   leaves existing records readable and no existing snapshots are rewritten.
2. Snapshot relation codec is stable and can decode old stored names.
3. Replacing/removing a file preserves the exact bytes of earlier snapshots;
   corruption/missing files fail validation rather than substituting latest bytes.
4. Cross-Space object references fail closed; failed upload/DB writes do not expose
   incomplete versions or delete reused historical objects.
5. Relevant package tests, all Go tests, vet/build, and isolated MySQL checks pass.
