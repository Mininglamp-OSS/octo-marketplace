# Marketplace name and publication improvements after PR147

## Goal
Deliver only the name reservation and publication workflow changes remaining after merged PR147 (55a8119). Its deletion protection, installation compatibility, authenticated frozen plans, current eligibility checks, version-aware graph projection and replay-safe frozen-plan migration are the baseline.

## Load-bearing behavior
- Preserve normalized per-Space/type draft and published name reservations from PR143, including preflight, backfill and rollback. Extend creation checks to Expert Teams, legacy rows and global market names without exposing foreign private names.
- Change published private/Space audience on the same immutable release through owner authorization, reference validation and policy-driven review. Preserve content, labels, release rows, fixed references and editable drafts.
- Persist editable draft changelogs independently of immutable release notes.
- Maintain Expert Team name columns on every stock migration write, preflight and rollback path.
- Execute terminal-card-receipt migration statements separately on one transaction connection, supporting the default driver without multiStatements. Preserve PR147 frozen-plan replay guards unchanged.

## Out of scope
No new deletion or installation implementation, no relaxation of root/node/edge eligibility, no duplication of PR142/146/147 commits. Web changes remain in companion PR1865. Runtime images and local gateway settings are unchanged.

## Acceptance
Focused packages and real MySQL regressions, full MySQL-backed Go suite, vet/build and OpenAPI check/diff must pass on this base. Preserve PR147 audience, retained mixed-pin publication, nullable legacy deletion, migration replay, public-alias retry and graph projection regressions. Additional retry scenarios from the former 857c1a9 must pass against PR147 eligibility checks without removing any predicate.

## Commit reconciliation
- Former 6082faf: retain only name/publication additions and necessary SQL/test integration changes; lifecycle/installation code is supplied by PR147.
- Former b7f709b: retain Expert Team stock-migration name reservation repair.
- Former 857c1a9: retain extra current-eligibility regression scenarios; drop the redundant root-audience predicate removal because PR147 already verifies the confirmed retry behavior with its full checks.

## Web metadata concurrency review
Expose an owner draft revision covering its saved content/version and mutable metadata/audience. Full-replace clients opt into expected_draft_revision; compare under the existing plugin row lock before writes, snapshots or audits. Existing clients remain wire-compatible. Web Skill/Expert metadata clients require a returned token and submit it with the replacement; stale reads return409 without silent retries. Tests must cover same-label edits, metadata without new snapshots, competing writers, foreign Space, and preserved newer state.

## Legacy-name review follow-up
The supported pre-backfill window leaves historical draft and published name keys NULL. Apply the same transactional, locked collision guard to standalone name-bearing updates and container rebuilds as to creation. Exclude the row itself and embedded children; preserve foreign-Space privacy and global unreleased draft-name privacy. Tests must cover Skill/Expert/Team draft and released names, Unicode-equivalent renames, service error mapping, legal self saves, and complete rollback of rebuild children, relations, snapshots, placements and audit evidence.

## Audience and editor recovery review follow-up
Narrowing and approved widening atomically synchronize the same owner's embedded graph with the root audience. Narrowing includes both live and released edges; widening promotes only the released topology. Never descend through standalone references or mutate foreign-Space/owner resources. Reuse normal publication's legacy/strict reference-rollout validation on request and approval. Skip soft-deleted names before backfill normalization. Return the revision of each successful update's committed head, computed before transaction commit with canonical JSON values, so clients can retry publication after an accepted save without reusing a stale revision. Verify read/graph/download/install privacy, automatic/manual legacy widening, strict pinned-reference rejection, retained version identities and post-save revision parity.

## Bounded current-head review corrections
Extend audience revocation to historical formally released embedded topologies; re-widening still uses only the selected release. Apply the existing legacy-name guard at frozen approval and release/adoption writes, and include Teams in the existing historical import name-column helper. Compute connector save revisions from the label actually committed without changing version pointers or snapshot-disabled callers. These close specific gaps in the declared features, not new installation/deletion semantics or a full lifecycle redesign. Validate transactional rollback, read/write revision parity and removed-child denial after replacement publication.
