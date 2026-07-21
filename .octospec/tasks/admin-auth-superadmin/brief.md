---
type: Task
title: "Task: admin-auth-superadmin"
description: Replace the shared-secret X-Admin-Token gate on /api/v1/admin/* with an octo-server session token + SuperAdmin role check, mirroring octo-server's /v1/manager/* surface.
tags: ["auth", "security", "admin", "load-bearing"]
timestamp: 2026-07-21T00:00:00+08:00
slug: admin-auth-superadmin
source: self
---

# Task: admin-auth-superadmin

## Goal

octo-admin's `system-mcp` page calls `/market/api/v1/admin/mcps` from the
browser with only the user's Octo session token (`token: <session>`). Today
that request is rejected with `401 AUTH_REQUIRED` because marketplace's admin
middleware requires `X-Admin-Token: $MARKETPLACE_ADMIN_TOKEN` — a shared
secret the browser cannot carry.

Align the admin gate with octo-server's `/v1/manager/*` convention: resolve the
caller's Octo session token via the existing identity resolver, then require
`identity.Role == "superAdmin"`. Retire the `MARKETPLACE_ADMIN_TOKEN` shared
secret and the ADMIN_OWNER_* companion knobs.

## Load-bearing behavior

- `/api/v1/admin/*` authentication:
  - **Before**: `X-Admin-Token` compared with `subtle.ConstantTimeCompare`
    against `MARKETPLACE_ADMIN_TOKEN`; success stamps a synthetic
    `admin`/`Admin` identity (or configured `ADMIN_OWNER_UID/NAME`).
  - **After**: session token via `Token` header (or
    `Authorization: Bearer …`) is resolved through the same
    `auth.Resolver` used by the public surface, then admitted iff the
    identity carries `Role == "superAdmin"`. Success stamps the real
    user's `{UID, Name}` identity with empty `space_id`.
- Downstream `service.CreateSystem` (`internal/service/mcp.go`) writes
  `caller.UID` → `owner_uid` and `caller.Name` → `creator_name` on system
  MCPs. These columns now reflect the real SuperAdmin caller instead of the
  synthetic `admin`/`Admin`. Existing rows are untouched.
- The uniqueness pre-check `SystemNameExists` / `SystemSlugExists` is
  name-only (not scoped by owner_uid), so no cross-admin naming regression:
  the previous "stable admin uid" assumption stays satisfied by the SQL, not
  by the caller identity.
- Error envelope on the admin surface:
  - `401 AUTH_REQUIRED` — missing/invalid token.
  - `403 FORBIDDEN` — token resolved but role is not `superAdmin`.
  - `503 UPSTREAM_UNAVAILABLE` — resolver call failed or context missing.
- Dev bypass (`AUTH_ENABLED=false`) still short-circuits the resolve+role
  check and stamps `DEV_AUTH_UID`/`DEV_AUTH_NAME` (falling back to
  `admin`/`Admin` inside the constructor) so local iteration does not need
  a live octo-server.
- CORS `Access-Control-Allow-Headers` no longer advertises `X-Admin-Token`
  (removed alongside the middleware) so a legacy frontend can't slip
  through preflight to a 401.

## Out of scope

- Bot-token access to admin routes (`bf_*`). Bot tokens hit the same
  session-token endpoint and will resolve to an empty identity; they will
  be rejected as invalid but there is no defense-in-depth allowlist for
  admin-role bots. Design that later if the need arises.
- A shared `roles` package or exported `RoleSuperAdmin` constant in
  octo-server. Marketplace keeps a local `const RoleSuperAdmin = "superAdmin"`
  in `internal/middleware/admin.go` with a comment mandating cross-repo grep
  before renaming.
- Any change to the `service.CreateSystem` uniqueness rule or its stale
  docstring at `internal/service/mcp.go:277-279` claiming per-owner
  uniqueness. The SQL was always global; that stale prose is a separate
  cleanup.
- Audit trail for admin actions. `creator_name` now carries the real user
  which is an audit improvement, but there is no separate immutable audit
  log yet.
- Fine-grained admin roles (e.g. `admin` for read-only surfaces).
  A future `RequireRole(role string)` middleware can compose on top of the
  shared `resolveUserIdentity` helper introduced by this task.

## Dependencies

- **octo-server role encoding.** `role == "superAdmin"` is coupled by
  convention to `octo-server/pkg/auth/tokeninfo.go`. If octo-server renames
  or re-cases the role, marketplace silently 403s every SuperAdmin until
  the constant is updated. The constant's doc comment records this
  obligation.
- **Resolver `?include=context`.** `HTTPResolver` at
  `internal/auth/resolver.go:38` already passes `include=context`, which is
  required for `identity.ContextIncluded == true` — the middleware treats a
  missing context as `503 UPSTREAM_UNAVAILABLE`.
- **octo-admin frontend.** The `system-mcp` page already sends the session
  token as `token: <hex>`; no frontend change required for the primary
  breakage. Any code path still emitting `X-Admin-Token` will fail 401
  after this change and must be updated (out of scope for this PR).

## Acceptance

- `internal/middleware/admin.go` implements: dev bypass → session-token
  resolve → `role == RoleSuperAdmin` → `setAuthContext(_, _, "")`.
- The resolve → validate chain is shared with `Authenticator.Handler()` via
  a package-private `resolveUserIdentity(c, resolver, token)` helper in
  `internal/middleware/auth.go`. No duplicated resolver-chain code between
  the two middlewares.
- `NewAdminAuthenticator(authEnabled=true, resolver=nil, …)` panics at
  construction (tested); misconfigured deploys fail at boot rather than
  serving 503s at request time.
- Config surface: `MARKETPLACE_ADMIN_TOKEN`, `ADMIN_OWNER_UID`,
  `ADMIN_OWNER_NAME`, `Config.AdminIdentity()` and the linked `ValidateAPI`
  guardrail are removed. `.env.example` and `scripts/restart-api.sh` no
  longer reference them.
- Tests:
  - `internal/middleware/admin_test.go` covers dev bypass, dev identity,
    nil-resolver panic, missing token (401), resolver error (503), empty
    UID (401), missing context (503), non-SuperAdmin (403), SuperAdmin
    happy path (200 + stamped identity).
  - `internal/api/router/router_test.go` covers missing-token 401,
    non-admin 403, superAdmin happy-path 200 through the full router chain.
  - `internal/api/integration/integration_test.go` covers admin category
    Create via `PublicWithDBAndAdminAuth` with a fake SuperAdmin resolver.
- Docs updated:
  - `docs/api/mcp-v1.md` §9.1 documents the new `Token` header + role
    requirement; §9.3 lists the new error codes; §9.5 replaces the
    "rotate the shared secret" prose with "revoke the SuperAdmin account
    in octo-server".
  - `README.md` admin-auth paragraph rewritten.
  - `.env.example`, `scripts/restart-api.sh` cleaned.
  - `docs/openapi/swagger.yaml` regenerated via `make openapi-gen`.
- `go build ./...`, `go test -count=1 ./...`, `go vet ./...`, `gofmt -l`,
  `make openapi-check` gates all clean (openapi-diff shows no breaking
  changes on the wire; only description text updates).
