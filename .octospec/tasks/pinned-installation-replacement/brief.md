# Pinned installation replacement delivery

## Goal
Replace PR126 while preserving its issue scope (#119, #121, #123; references
#120, #122, #114), incorporating earlier fixes and the review on `639d1f1`.

## Load-bearing list
- Refuse old inline capability snapshots in root/member resolution for both
  installers, with no Fleet/provisioner mutation; hidden roots remain 404.
- Preserve exact released pins and all earlier scope/receipt/audit guarantees.
- Freeze verified file bytes before inventory locks, reject changed sources and
  stale preview digests, and document the remaining full-inventory lock scope.
- Add an executable read-only activation gate over current released graphs,
  documented rollout prerequisites and safe expired-key reconciliation steps.
- Fail on missing legacy skill references, remove dead routing, and derive
  rehearsal counts from actual verified database results rather than constants.

## Out of scope
Production operations or activation, automated deletion/reset of spent keys,
changing Fleet protocols, claiming fixture checks prove real provider execution.

## Acceptance
Focused negative tests for inline snapshots, root 404s, unknown skill references,
file I/O before locks and concurrent source changes. Real MySQL verifies readiness
before/after migration and a corrupt pinned member. Full Go suite, focused races,
Vet, build, OpenAPI checks and rehearsal script/report unit checks pass. Publish a
new PR against main with the same issue links, then close the superseded PR if
the authenticated account is allowed to do so.
