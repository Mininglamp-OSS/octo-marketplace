# Pre-PR self-audit — capability installation

Updated: 2026-09-24 (initial audit: 2026-09-23). Repository: octo-marketplace.
Branch: `codex/plugin-capability-install`.
Base: `upstream/main` at `933ced9c62eb7e5a6153a46c8e4e0ca864b25add`
(refetched during initial review). Current candidate: `ae7fb71` plus the
post-PR fixes below; earlier sections retain their original verification history.

This is a pre-PR self-audit, not formal approval. The initial two rounds included
independent read-only passes over Definition/assets and Fleet/HTTP/auth, followed
by primary review and regression tests. The 2026-09-24 adaptation received another
independent final read-only review of the combined changes. That initial audit
missed the member-description and replacement-budget issues subsequently raised
on the PR. The current post-PR review is recorded below. The contract is
source-verified against Fleet `origin/test` at `49a8266`; the integration stays
disabled by default.

## Post-PR review response — 2026-09-24

Current explicit user decisions supersede the earlier counting/compatibility
assumptions. These changes are confined to the new route; no legacy installer,
ingestion/backfill, Fleet code, database or deployment is modified.

- **MCP support:** keep rejecting a command-based server with non-empty `type`
  (including `"stdio"`) or headers before Fleet. This is intentional, not an
  ingestion change or a silent normalization. Fleet `49a8266`
  `pkg/capability/validation.go` also rejects these fields. Service regressions
  pin both cases for single experts and team members, alongside the existing
  parser tests; API documentation now explicitly states the restriction.
- **Member descriptions:** retain the user-requested per-member own-manifest
  behavior and the team/single-expert assertions plus 255/256-rune limits. The
  reviewer's corrected legacy trace is right: `agentSpecFromPlugin` is only the
  single-expert path, and legacy squad provisioning uses the team summary.
  This is an intentional new-route metadata improvement, not a claimed legacy
  regression and not authorization to alter the old route.
- **Skill budget:** refund only the validated archived SKILL.md charge before
  fetching its authoritative replacement. ZIP decompression, path safety,
  object authorization, per-file and aggregate bounds remain enforced. New
  regressions reproduce the old false 413, cover exact-budget success and a
  one-byte excess, and assert the preserved supporting file and final budget.
- **Counting:** call the existing best-effort tracker once for each successful
  invocation, including explicit replays and missing replay metadata. This
  counts API successes, not unique resources. Tests cover expert/team, two
  repeated successes, fresh/replayed/unknown metadata, and no count on failure.
  Resource idempotency and response replay-header semantics remain unchanged.
- **Timeout:** the new operation and HTTP call share a two-minute upper bound.
  Copy only the HTTP client policy for the capability call; keep shared
  transport/redirect protection and the legacy 30-second timeout unchanged.
  Tests check the actual request-context deadlines, shorter caller deadlines,
  cancellation and single-send behavior without wall-clock sleeps.
- **Bot guards:** add a real-authenticator handler test with an authenticated
  Bot token and a direct-service test. Both prove rejection before the next
  boundary; the service also asserts no Fleet mutation or metric increment.

The counting and budget tests failed on the previous implementation; the
timeout test observed the old 30-second deadline. All pass after the scoped
fixes. Independent Definition/assets and Fleet/HTTP/auth reviewers found no
remaining confirmed P0/P1 within these changes. This is not formal approval.

Other review observations were checked, not blindly applied: empty team
instructions are allowed by Fleet and retain current behavior; 409 display text
stays verbatim by product requirement and its plain-text rendering contract was
already documented. Non-nil unsupported installer wiring diagnostics and
duplicate caller-supplied `custom_env` keys remain non-blocking hardening items;
the concrete production Fleet client implements the installer and failure is
closed. No unrelated parser or router refactor is included.

Verification for this candidate: focused tests and independent race checks,
`LOG_FORMAT=console go test -race -shuffle=on -count=1 ./...`, `go vet ./...`,
`CGO_ENABLED=0 go build ./...`, and golangci-lint v2.12.2 (0 issues) passed.
`make openapi-check` passed with 100% handler coverage, no generated drift and
the same 10 pre-existing lint warnings. `make openapi-diff BASE_REF=upstream/main`
passed with no breaking changes. The OpenAPI commands used the existing
temporary PyYAML virtualenv and did not alter repository tool dependencies.

Prior authorized live testing (not rerun for these review fixes) confirmed an
MCP/environment expert and a two-member MCP/environment team, preserved member
metadata and same-key IDs. Skill-bearing installs failed in Fleet with PostgreSQL
42P10: its deployed skill uniqueness indexes do not match the ON CONFLICT target,
despite migration 143 being recorded as applied. This is an external rollout
blocker, not a Marketplace payload fix; Fleet requires a corrective migration.
Test data was retained as requested. Negative Workspace/runtime authorization
and timeout recovery still require live verification before enabling the gate.

## Fleet test adaptation — 2026-09-24

Authoritative source: fetched `origin/test` from `dmwork/octo-fleet`, commit
`49a8266` (includes `14b01dc`, unified capability installation). Source was read
from that revision without changing Fleet's working tree. The earlier HTML and
older capability-installation worktree are not the implementation contract.

| Boundary | Verified Fleet source | Marketplace adaptation |
| --- | --- | --- |
| Environment and team metadata | `pkg/capability/definition.go` | `custom_env` belongs to every Expert Definition; runtime bindings contain only expert name/runtime ID; preserve team description/instructions |
| Success response | `internal/publicapi/facade.go` and `internal/handler/capability_installation.go` | Decode the public `data` envelope and retain result identity/reference validation; reject bare, null or malformed results |
| Text limits | `pkg/capability/validation.go` | Names 128 characters, Expert/Team descriptions 255, Skill descriptions 512, roles 500; reject without truncation |
| MCP structure | Definition validation and `openapi/schemas/capability-definition.schema.json` | Omit known empty defaults; validate closed stdio/remote shapes and 64 KiB normalized limit; preserve env/header/argument values |
| Skill entry document | Definition validation | Reject supporting files that case-insensitively alias `SKILL.md` |
| Idempotency | Handler and `migrations/223_capability_installation_receipt.up.sql` | Normalized-request fingerprint, 24-hour retention; 409 `DUPLICATE` plus allowlisted idempotency conflict classification |
| Replay metadata | No `Idempotency-Replayed` emitted by the verified implementation | Missing/invalid/ambiguous metadata remains unknown; the later explicit product decision counts every successful invocation independently |

MCP parsing also rejects duplicate/unknown fields, null values, malformed UTF-8
and unpaired surrogate escapes instead of silently changing configuration.
Surrogate regressions were reproduced failing before the scoped decoder fix,
then passed. Empty strings, whitespace, valid Unicode and escaped literals retain
their values. No dependency or generic parsing framework was introduced.

HTTP 409 still preserves Fleet's valid message verbatim. Only the fixed
`details.resource == idempotency_key` classification is retained, not arbitrary
upstream details or the raw key. All other generated client-display errors stay
Chinese; no raw upstream errors or submitted secrets enter logs.

The initial adaptation suppressed installation counts without reliable replay
metadata. The explicit post-PR product decision above replaces that policy with
the legacy successful-invocation count; no receipt persistence is introduced.

## Confirmed findings and scoped fixes

### Post-PR B1 — member descriptions — 2026-09-24

The PR review found a missed P1: team assembly passed the root team description
to every member Expert Definition. The one-line fix reads each member's own
manifest description; team-level and single-expert descriptions stay unchanged.
No legacy installer, route, schema, error handling or Fleet code changed.

Regressions failed before the fix: Alice received `team summary` instead of
`expert summary`, and a 256-rune member description reached Fleet. They now pass
with distinct team/Alice/Bob descriptions, the existing full-request JSON
stability assertion after relation reordering, and 255/256-rune member limits.
The single-expert test also explicitly checks its own description.

Independent read-only review found no further blocker in this fix. Verification:
focused regressions, `LOG_FORMAT=console go test -race -shuffle=on -count=1 ./...`,
`go vet ./...`, `CGO_ENABLED=0 go build ./...`, and golangci-lint v2.12.2 all passed.
`make openapi-check` and `make openapi-diff BASE_REF=upstream/main` passed with no
schema drift or breaking change and the same 10 existing lint warnings. The
initial OpenAPI attempt lacked PyYAML; rerunning in the existing temporary
virtualenv resolved the environment failure without changing repository tools.
This verification does not include a live team installation or formal approval.

### Post-PR gateway smoke fix — 2026-09-24

**P1 — The new path did not account for the existing OCTO gateway mount.**
With the shared base ending in `/fleet`, the new adapter requested
`/fleet/v1/capabilities/install` and received 405 HTML. The gateway exposes
`/fleet/api/v1/capabilities/install`; the Fleet source still correctly registers
`/v1/capabilities/install` for direct service access. Only the new adapter now
adds `/api` when the parsed base path ends in `/fleet`. It does not mutate the
shared base, add configuration, change legacy paths, probe routes or retry.

The new regression failed for gateway/nested-gateway cases before the fix.
It now passes alongside direct service, hostname `fleet`, non-matching paths,
same-client legacy agent/skill/squad calls and forwarded identity/idempotency.
Independent read-only review found no further confirmed issue.

A temporary harness using the actual adapter and configured test gateway now
receives canonical 401 `AUTH_REQUIRED`, rather than 405 HTML. It sends no
credentials, Workspace/runtime or installable Definition, so no resource was
created. The local service was rebuilt/restarted on port 8092 with the gate
enabled only in that process; readiness and 10 local negative/CORS HTTP checks
passed. Committed defaults remain disabled and full authenticated installation
is still unverified.

Full race tests, vet, build, golangci-lint v2.12.2 (0 issues), OpenAPI check and
breaking-change diff were rerun successfully for this fix. OpenAPI remains
unchanged, with the same 10 pre-existing lint warnings.

### Earlier review fixes

1. **P2 — Preparation failures were reported as uncertain Fleet outcomes.**
   Artifact/DB failures before the adapter call fell into the same 503 branch as
   transport failures after a potential commit, without safe cause diagnostics.
   The service now marks errors after invoking the Fleet adapter; local preparation
   failures return 500 with `phase=preparation`, uncertain attempts remain 503 with
   `phase=fleet`, and phase/reason/status are logged without raw errors or content.
   Known typed errors retain their status mapping. Same-key retry guidance is
   preserved even for a preparation failure on a retry of an earlier attempt.

2. **P2 — Legacy ZIP extraction used a fixed expansion cap instead of the
   remaining installation budget.** The old helper argument limited compressed
   bytes, allowing the full over-budget tree to be allocated before rejection.
   Only the new path now uses the existing aggregate-bounded extractor, keeping
   the single-SKILL.md, path safety, duplicate, rooting, and ordering checks.
   Regressions cover compressed-small/expanded-large input, missing/multiple
   entry documents, duplicate/traversal/rooting collisions, and stable bytes.

3. **P2 — A declared storage-backed MCP was silently omitted.**
   `capability_definition.go` reused `rawAttachmentContent`, which treats absent
   and storage-backed attachments alike. The new builder now rejects a declared
   non-inline MCP with `definition.experts.mcp_config / unsupported_source` before
   Fleet. This deliberately does not add storage-MCP loading or change legacy
   installation. Regressions cover single expert and team, absent MCP, and raw MCP.

4. **P1 — Environment values could be silently changed during JSON decoding.**
   `installations.go` decoded values directly into `map[string]string`: null
   became an empty string; invalid UTF-8 and unpaired surrogate escapes became
   replacement characters before service validation. This could install a wrong
   credential/configuration. The new route's `installationEnvValue` decoder now
   rejects those inputs while preserving legitimate empty strings, whitespace,
   Chinese, surrogate-pair emoji, U+FFFD, and literal backslashes. The public
   schema remains a string map. No shared or legacy decoder changed.

Both second-round findings were reproduced by failing regressions on the prior
code, then passed after the scoped fixes. The modified boundaries received
another independent read-only check; no additional confirmed findings remained.

## Explicit product changes during review

- HTTP 409 preserves Fleet's valid nonblank `error.message` verbatim. Missing or
  invalid message uses a Chinese fallback. `Code` and conflict details remain
  stable; the Go error string and logs never include the upstream message.
- Other generated `message` and `hint` values are Chinese for client display.
  Auth failures can precede the handler, so a request-local display override is
  set only on the exact new POST route before authentication. No authentication
  decision, status, identity, or old-route message is changed.
- Strict JSON decoding is local to the new route so validation errors are also
  Chinese without changing the legacy decoder. 429 Retry-After is documented.
- No Client/Fleet implementation, deployment, default enablement, schema
  migration, legacy installer, or unrelated refactor was added.

## API standard review

| Dimension | Result | Evidence |
| --- | --- | --- |
| URL / operationId — R6/R10/R12 | Pass | Resource subroute, relative path, `plugin.installation.create` |
| Envelope — R1 | Pass | `data` success, structured `error` failure |
| Naming — R3/R7/R8 | Pass | snake_case, named plugin ID; no new time fields |
| Error codes/auth — R2/R4 | Pass with product exception | Fixed code enum; Chinese text explicitly requested; 409 Fleet wording preserved |
| swag — R13 | Pass | Route, body, headers, status responses and security documented |
| Pagination/batch — R5/R11 | Not applicable | One synchronous installation command |

## Verification — final adaptation

- `LOG_FORMAT=console go test -race -shuffle=on -count=1 ./...`: PASS.
- Focused Fleet, service, handler, router, model, contract-limit and MCP tests:
  PASS, including an independent final affected-package run. MCP-focused tests
  passed three times.
- Source-contract audit: PASS. Exported the unmodified `pkg/capability` and
  Definition schema from Fleet `49a8266` into a temporary module. Marketplace's
  actual assembled JSON passed Fleet's strict `InstallRequest` decoder and
  `NormalizeAndValidateDefinition` for six cases: single expert/two-member team
  crossed with empty object/empty servers/combined stdio+remote MCP. Fixtures
  included shared Skills, 128-character Unicode names and custom environment
  values containing Chinese, emoji and whitespace. The source package's own
  tests also passed. The temporary harness was not added as a dependency or
  repository file; this is source validation, not a live installation test.
- Prior 2026-09-23 environment-decoder fuzz evidence (not rerun in this pass): `go test
  ./internal/api/handler/plugin -run '^$' -fuzz '^FuzzInstallationEnvValue$'
  -fuzztime=10s -parallel=2`: PASS, 254,862 executions; parser and valid-Unicode
  round-trip checks found no panic or data change.
- `go vet ./...`: PASS.
- `CGO_ENABLED=0 go build ./...`: PASS.
- `go run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2 run
  --timeout=5m`: PASS, 0 issues (same version as CI).
- Edited Go files: gofmt clean; `git diff --check`: PASS.
- `make openapi-check`: PASS after committing implementation and generated spec
  together; the earlier uncommitted-baseline failure is resolved.
  Lint reports 0 errors and 10 pre-existing warnings on unrelated operations.
- `make openapi-diff BASE_REF=upstream/main`: PASS, no breaking changes.
- OpenAPI commands used a temporary Python virtualenv containing PyYAML; no
  global Python packages or repository tool dependencies were changed.
- Final PR preflight reran race tests, vet and build successfully after OpenAPI
  generation finished. An initial concurrent run read the intermediate generated
  schema before normalization and failed the schema assertion; no code change was
  needed. Do not run spec generation concurrently with schema-reading tests.
- `TEST_MYSQL_DSN` was not configured locally; real MySQL integration cases remain
  for CI. Source-contract checks do not replace live Fleet integration.

## Remaining handoff

The user authorized PR creation on 2026-09-24. Review fixes and regenerated
OpenAPI are committed together, and the baseline gate now passes. This record
captures local PR preflight, not remote CI results or formal approval.
Independent human review/CI is still required. The user explicitly requested no
issue association; the repository's Sprint gate remains unsatisfied without it.

Before enabling: verify real-service auth and runtime access, single-expert/team
installation, result shape, atomic behavior, same-key replay, changed-input
conflict and timeout recovery. Successful retries intentionally count again per
the updated product requirement. Retry guidance uses the actual 24-hour window;
after expiry, reconcile the Workspace before starting a replacement operation.
The text-only asset restriction, unsupported storage-backed MCP and lack of
historical Definition snapshots are intentional and documented in
`docs/api/plugin-installations.md`. Prior real-service results and the remaining
Fleet Skill schema blocker are documented above. No Client change, Fleet source
change, deployment or default enablement is included in this PR.
