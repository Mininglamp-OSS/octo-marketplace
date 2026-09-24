# Pre-PR self-audit — capability installation

Updated: 2026-09-24 (initial audit: 2026-09-23). Repository: octo-marketplace.
Branch: `codex/plugin-capability-install`.
Base: `upstream/main` at `933ced9c62eb7e5a6153a46c8e4e0ca864b25add`
(refetched during review). Candidate: `40c8c93` plus the current review fixes
and Fleet test contract adaptation.

This is a pre-PR self-audit, not formal approval. The initial two rounds included
independent read-only passes over Definition/assets and Fleet/HTTP/auth, followed
by primary review and regression tests. The 2026-09-24 adaptation received another
independent final read-only review of the combined changes. No remaining confirmed
P0/P1 was found. The contract is now source-verified against Fleet `origin/test`
at `49a8266`; real deployment/runtime integration remains unverified, and the
integration stays disabled by default.

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
| Replay metadata | No `Idempotency-Replayed` emitted by the verified implementation | Missing/invalid/ambiguous metadata is unknown, not a fresh install; do not increment new-route Marketplace metrics |

MCP parsing also rejects duplicate/unknown fields, null values, malformed UTF-8
and unpaired surrogate escapes instead of silently changing configuration.
Surrogate regressions were reproduced failing before the scoped decoder fix,
then passed. Empty strings, whitespace, valid Unicode and escaped literals retain
their values. No dependency or generic parsing framework was introduced.

HTTP 409 still preserves Fleet's valid message verbatim. Only the fixed
`details.resource == idempotency_key` classification is retained, not arbitrary
upstream details or the raw key. All other generated client-display errors stay
Chinese; no raw upstream errors or submitted secrets enter logs.

The current lack of replay metadata means the new route temporarily does not
update Marketplace installation counts, even on first success. Installed resources
and legacy counts are unaffected. This avoids duplicate counting on retries
without adding out-of-scope receipt persistence. Restore counting only after a
reliable upstream non-replay signal is available.

## Confirmed findings and scoped fixes

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
Independent human review/CI is still required. No tracking issue was supplied;
the repository's Sprint gate requires an appropriate issue/board assignment.

Before enabling: verify real-service auth and runtime access, single-expert/team
installation, result shape, atomic behavior, same-key replay, changed-input
conflict and timeout recovery. Confirm the temporary installation-count limitation
or obtain reliable replay metadata. Retry guidance uses the actual 24-hour window;
after expiry, reconcile the Workspace before starting a replacement operation.
The text-only asset restriction, unsupported storage-backed MCP and lack of
historical Definition snapshots are intentional and documented in
`docs/api/plugin-installations.md`. No real-service installation, Client change,
Fleet source change, deployment or default enablement was performed.
