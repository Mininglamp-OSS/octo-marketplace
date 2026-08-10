---
type: Task
title: "Task: mcp-ssrf-namespace-guards"
description: Harden the MCP create/update and probe surfaces against SSRF and official-namespace hijacking (pentest 4.6/4.7, issues #48/#49).
tags: ["security", "api", "ssrf"]
timestamp: 2026-08-10T00:00:00+08:00
slug: mcp-ssrf-namespace-guards
source: self
---

# Task: mcp-ssrf-namespace-guards

## Goal

Close two findings from the Octo-web penetration report (2026-07-31) on the MCP
catalog surface, without changing the API contract, DB schema, or the existing
permission model:

- **4.7 / #48** — a normal user could register a `public` MCP whose remote URL
  pointed at internal/loopback/metadata addresses (SSRF), and could reuse the
  exact name or slug of an official `system` MCP (impersonation).
- **4.6 / #49** — the `_probe` SSRF defence was sound but had residual gaps:
  mixed public/private DNS resolutions were not rejected wholesale, and internal
  DNS/connection error text was echoed to the client.

Reuse the Probe subsystem's existing SSRF primitives (`validateProbeURL`,
`isUnsafeProbeIP`) rather than inventing a parallel policy.

## Load-bearing behavior

- **Create/update URL validation** (`POST /market/api/v1/mcps`,
  `PATCH /market/api/v1/mcps/{id}`, and the admin `system` twins): for remote
  transports (`streamable-http` / `sse`) the connection URL is validated with
  the same policy Probe uses —
  1. literal-IP / scheme / credential check via `validateProbeURL`, and
  2. a best-effort DNS resolve-time check that rejects the write if any resolved
     address is unsafe. The resolve step is **fail-open** (lookup error/timeout/
     empty answer does not block the write) because the runtime that actually
     dials owns the authoritative gate and DNS can rebind after the check.
  Rejection is `VALIDATION_ERROR` (400) with `field=url, reason=private_address`.
  The `PROBE_ALLOW_PRIVATE` escape hatch applies identically. On `Patch` the URL
  check runs only when the patch touches `transport`/`url`/`command` (matching
  the existing required-field gating); it does not retro-scan untouched rows.
- **Official-namespace protection**: `Create` and `Patch` reject a name or slug
  that collides with a live `visibility=system` row, reusing `checkSystemDupes`.
  The rejection is `DUPLICATE` (409) with `reason=official_namespace` and a
  message that names the official catalog (not "in this Space", which would
  misdirect since system rows are spaceless). On `Patch` name and slug are
  checked **independently**, and each only when that field's merged value
  actually changed (not merely when supplied), passing `exceptID=m.ID`: a patch
  that changes neither cannot introduce a collision, so a row that already
  shares an official name/slug stays editable — including by full-object PATCH
  clients that re-send unchanged fields — and editing the non-colliding field is
  never refused because of the other. An owned system row never self-collides.
- **Probe whole-request rejection**: the dialer resolves the host and rejects the
  entire request if ANY resolved address is unsafe (no cherry-picking a safe IP
  out of a mixed answer). DNS failure / empty answer is rejected. The unsafe-IP
  predicate derives the embedded IPv4 from the IPv4-mapped and IPv4-translated
  (`::ffff:0:0:0/96`) forms and the NAT64 well-known prefix (`64:ff9b::/96`) and
  re-checks it, so a translated loopback/RFC-1918 address cannot pass as public
  IPv6. Open: the RFC 8215 local-use `/48` prefix is only checked on its trailing
  32 bits, not decoded per RFC 6052, so a `/48` encoding at another offset can
  still slip; `fec0::/10` and `240.0.0.0/4` are also not yet covered (review
  P2-A, tracked separately).
- **Probe error redaction**: SSRF-policy rejections and any socket-level failure
  (`*net.OpError`, including one wrapped in `*url.Error`) collapse to a single
  opaque client message (`probe target is not reachable`); the concrete cause is
  logged server-side only. Application-level causes (non-2xx status, JSON-RPC
  error, malformed payload) keep a concrete hint. Open: non-`OpError` transport
  failures such as TLS/certificate errors also still surface a concrete message
  (review P2-B, tracked separately). Redirect hops keep re-running the per-hop
  literal check, and the redirected host is re-validated at dial time.

## Out of scope (deliberately not touched)

- **Visibility tightening (report 4.7 rec #1)**: normal users still create
  `public`. Restricting them to `private` / admin-only public is a product
  decision, deferred.
- **Global slug uniqueness (report 4.7 rec #3)**: only official (`system`)
  name/slug is protected; cross-Space/owner same-name `public` rows remain
  allowed (normal multi-tenant semantics).
- **Runtime-side SSRF gate**: the authoritative resolve-time check when
  octo-cli / agent actually connects is owned by that runtime, not Marketplace.
- **Probe rate limiting (report 4.6 rec #5)**: separate follow-up.
- No API contract, DB schema, or permission-model changes.

## Acceptance

- Create/patch with a literal internal/loopback/metadata/link-local URL →
  `VALIDATION_ERROR` `private_address`; a legitimate public URL succeeds; `stdio`
  is unaffected; `PROBE_ALLOW_PRIVATE=true` permits private targets.
- Create/patch where the host resolves to any unsafe address → rejected; a DNS
  error fails open (write succeeds).
- Create/patch reusing a `system` name or slug → `DUPLICATE`; a no-op edit of an
  owned `system` row does not self-collide.
- Probe rejects mixed public/private, all-private, IPv6-loopback, cloud-metadata,
  and DNS-failure resolutions, and never leaks resolved addresses / DNS detail.
- Service-level tests cover the above (positive + negative); existing probe
  tests updated for the new dialer signature and opaque error.
- `go build ./...`, `go test ./...`, `go vet`, `gofmt`, and `make openapi-check`
  (no drift) all pass.
