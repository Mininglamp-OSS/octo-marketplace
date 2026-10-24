# Versioned marketplace references

## Goal
Persist independent expert and skill references to immutable released versions, with ID-only current connector references, while keeping legacy behavior behind a disabled rollout gate.

## Background
Legacy containers own embedded children and resolve mutable target IDs. Independent resources require explicit released pins, audience checks and historical reverse indexes before enabling the write path.

## Load-bearing behavior
- Resource ownership and audience remain authoritative and are rechecked under transactional locks.
- New expert/team edges cannot reference embedded resources or carry inline capability overrides.
- Every newly saved reference snapshot receives an immutable reverse index in the same transaction.
- Publishing and approval validate frozen references, not a later editable graph.
- Ever-published resources cannot be deleted or narrow scope; shared targets are never cascade-deleted.

## Out of scope
Fleet protocol changes, enabling rollout, remote writes, production migrations and ambiguous historical pin inference.

## Acceptance
Owner-private positive and cross-Space negative tests; invalid/unreleased/wrong-resource pins rejected; connector versions rejected; immutable snapshot/index agreement; publication/approval revalidation; lifecycle regressions pass with gate disabled. OpenAPI regenerated and checked for DTO additions.
