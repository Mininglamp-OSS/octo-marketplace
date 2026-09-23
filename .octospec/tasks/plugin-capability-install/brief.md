# Parallel plugin installation endpoint

## Goal

Add `POST /v1/plugins/{plugin_id}/installations` alongside `/plugins/install`.
Marketplace resolves the visible plugin graph and text assets, applies the
requested resource name, and sends one atomic capability-install command to
Fleet on behalf of the authenticated user. The existing installer is unchanged.

## Load-bearing behavior

- Caller-scoped root/dependency reads and managed object-storage boundaries.
- Deterministic Definition assembly, shared Skill deduplication, complete text
  files, per-member MCP configuration, team instructions, and environment binding.
- One Fleet mutation, end-to-end Idempotency-Key, no fallback to the old installer
  after an uncertain outcome, and replay-aware best-effort metrics.
- Backward-compatible agent_id / squad_id response envelope.
- Disabled by default until the upstream contract is confirmed.

## Pending upstream contract

The 2026-09-23 proposal does not yet define `experts[].mcp_config`,
`expert_team.description/instructions`, or `bindings.experts[].custom_env`.
Their proposed wire shape is isolated in `internal/fleet/capability_types.go`.
Do not enable `OCTO_FLEET_CAPABILITY_INSTALL_ENABLED` before those fields and the
direct service route `/v1/capabilities/install` are verified against Fleet.

## Out of scope

Client changes, enabling/deploying the new route, deleting/deprecating the old
route, modifying Fleet, binary Skill assets, and Marketplace installation-history
persistence. Reusing a key after changing the source graph may produce a safe
conflict; no historical Definition snapshot is stored by this endpoint.

## Acceptance

- Old and new routes coexist; disabled mode cannot issue a Fleet mutation.
- Single expert and team installs preserve names, instructions, MCP, skills/files,
  shared runtime and custom_env. Installation names do not mutate catalog data.
- Hidden dependencies, unsafe/missing/corrupt/binary assets, duplicate names,
  invalid topology, and size-limit violations fail before the Fleet call.
- Identical inputs and source content serialize identically; Fleet replays keep
  their IDs and do not increment Marketplace's metric again.
- Fleet failures/malformed replies never trigger a second installation path.
- Go tests/build/vet, generated OpenAPI, lint, and additive API diff are checked.
