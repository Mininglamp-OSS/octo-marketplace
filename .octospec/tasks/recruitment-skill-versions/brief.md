# Recruitment Skill source versions

## Goal
Carry each installed Skill's source version and marketplace ID through the
capability installation request used by web expert and expert-team recruitment.

## Load-bearing behavior
- Send `definition.skills[].version` (nullable opaque text) and `market_skill_id`.
- Read metadata from the same authorized Skill snapshot as the installed files.
  Pinned installs use the selected release's declared version, never the live draft.
- Reject non-null empty, padded, invalid UTF-8 or NUL-containing versions before
  Fleet mutation. Do not invent a version or use an expert/team version.
- Preserve the pinned installation naming algorithm, bindings, tenant visibility,
  retry identity, feature gates and legacy install route.

## Out of scope
Fleet changes, enabling rollout flags, migrations, Skill naming changes, automatic
updates, and public Marketplace request/response schema changes.

## Acceptance criteria
- Expert and team requests carry source metadata, including explicit null versions.
- Both compatibility and pinned capability builders are covered; a newer live
  draft cannot change the metadata of a selected released Skill.
- Invalid versions stop before Fleet and existing isolation tests remain green.
- Focused tests, `go test ./...`, OpenAPI check and comparison pass.
- Deploy Fleet's version-aware capability installer (migration 225) before callers.
