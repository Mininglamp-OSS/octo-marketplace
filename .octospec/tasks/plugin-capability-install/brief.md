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
  after an uncertain outcome, and no install count without reliable non-replay metadata.
- Backward-compatible agent_id / squad_id response envelope.
- Chinese client-display errors on the new route (including authentication),
  with Fleet's 409 error.message preserved verbatim per the user's requirement.
  Other upstream/raw internal messages must not reach the response or logs.
- Separate preparation failures from uncertain Fleet attempts, with safe
  phase/reason diagnostics and unchanged same-key retry rules.
- Disabled by default until deployment/runtime integration is verified.
- Reuse the legacy Fleet base URL: the known `/fleet` gateway mount uses
  `/api/v1/capabilities/install`; direct Fleet retains `/v1/capabilities/install`.
  Do not change legacy paths, add configuration or probe/retry alternate routes.

## Upstream contract

Source verified against Fleet `origin/test` at `49a8266` on 2026-09-24. MCP and
Team description/instructions are implemented; `custom_env` belongs to each
Expert Definition, not runtime bindings. The public route wraps results in data.
Normalized-request idempotency lasts 24 hours and conflicts use DUPLICATE with
details.resource=idempotency_key. Fleet emits no replay header, so the new route
skips Marketplace counting for unclassified success responses. The legacy count
is unchanged. Local smoke testing is authorized; production enablement and
installation into an unconfirmed Workspace/runtime remain out of scope.

## Out of scope

Client changes, production enablement/deployment, deleting/deprecating the old
route, modifying Fleet, binary Skill assets, and Marketplace installation-history
persistence. Reusing a key after changing the source graph may produce a safe
conflict; no historical Definition snapshot is stored by this endpoint.

## Acceptance

- Old and new routes coexist; disabled mode cannot issue a Fleet mutation.
- Single expert and team installs preserve names, instructions, MCP, skills/files,
  shared runtime and custom_env. Installation names do not mutate catalog data.
- Hidden dependencies, unsafe/missing/corrupt/binary assets, duplicate names,
  invalid topology, and size-limit violations fail before the Fleet call.
- Declared MCP configurations with unsupported sources cannot be silently omitted;
  null or malformed-Unicode environment values cannot become different strings.
- Identical inputs and source content serialize identically; Fleet replays keep
  their IDs. Unknown replay status never increments Marketplace install metrics.
- Empty MCP defaults are omitted; non-empty MCP and text limits match Fleet's
  verified contract without silently dropping fields or truncating descriptions.
- Fleet failures/malformed replies never trigger a second installation path.
- Legacy ZIP extraction enforces the remaining aggregate budget while reading,
  before materializing a full over-budget tree.
- 409 messages preserve upstream wording; other generated message/hint text is
  Chinese. Existing endpoints and their authentication messages remain unchanged.
- Go tests/build/vet, generated OpenAPI, lint, and additive API diff are checked.
