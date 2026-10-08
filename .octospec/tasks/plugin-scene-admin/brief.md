---
type: Task
title: "Task: plugin scene administration"
description: Add Marketplace scene configuration and admin-managed Plugin placement relationships.
tags: ["plugin", "marketplace", "admin", "placement"]
timestamp: 2026-10-08T10:30:00+08:00
slug: plugin-scene-admin
source: user
---

# Task: plugin scene administration

## Goal

Let platform operators manage Marketplace scene codes in `octo-admin` and
control which Plugins are placed in each scene. Skill, Connector (MCP), Expert,
and Expert Team catalog lists continue to select a scene through `scene_code`.

## Load-bearing behavior

- Scene and placement mutations are available only through the authenticated
  Marketplace admin surface.
- `scene_code` is a stable, unique machine identifier; edits change display
  metadata, not the code.
- The `default` scene remains the compatibility fallback: it is seeded by the
  migration, cannot be deleted, and existing create/publish behavior continues
  to place Plugins there.
- A Plugin can be placed in multiple scenes, with at most one live placement
  per Plugin and scene.
- Placement writes validate that both the scene and a live, non-embedded Plugin
  exist. Cross-Space Plugin administration remains restricted to the existing
  system-admin boundary.
- Deleting a scene that still has visible/live Plugin or category placements
  fails closed with `CONFLICT`; stale references that cannot appear in the
  admin surface are cleaned up transactionally when the scene is deleted.
- Catalog reads keep applying Space visibility and published-state rules in
  addition to the requested `scene_code`; scene selection never bypasses auth.
- API success/error envelopes and field naming follow the OCTO OpenAPI rules.

## In scope

- A `plugin_scenes` registry with code, display name, description, sort order,
  timestamps, and usage counts.
- Admin CRUD for scenes.
- Admin list/create/update/delete for Plugin placement relationships.
- A channel-maintenance tab in the existing Skill Market page; no additional
  top-level administration menu.
- Multi-select batch placement controls in the Skill, MCP, Expert, and Expert
  Team lists.
- A `scene_codes` projection on admin Plugin list items so those four lists
  show each Plugin's configured channels without per-row requests.
- English and Chinese UI strings, focused backend/frontend tests, and updated
  generated OpenAPI output.

## Out of scope

- Changing the tenant-facing Marketplace list contract beyond its existing
  `scene_code` query parameter.
- Making `octo-web` choose a scene dynamically.
- Per-scene authorization policy, pricing, promotion schedules, or arbitrary
  client-supplied scene configuration objects.
- Managing category-to-scene placement in this first admin surface.
- Removing the default scene or changing existing automatic default placement.

## Acceptance criteria

- Operators can create, list, edit, and delete unused non-default scenes.
- Operators can list placements and add, edit, or remove non-default
  Plugin/scene relationships for all four Plugin types.
- Duplicate scene codes and duplicate Plugin/scene relationships return 409;
  missing resources return 404; invalid payloads return 400.
- Existing Plugins and placements are represented by the migration, including
  the `default` scene.
- The admin UI exposes scene maintenance as a market tab and atomic batch
  add/remove placement actions directly in each Plugin-type list.
- Every Plugin-type list shows the configured channel codes for each row.
- Relevant Go and TypeScript tests pass; OpenAPI check/diff results are
  recorded, including any pre-existing toolchain blockers.
