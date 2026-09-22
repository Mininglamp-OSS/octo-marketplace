---
type: Task
title: "Task: reuse existing skills during plugin install"
description: Let the unified plugin install bind an exact-name workspace Skill instead of failing on Fleet's duplicate-name conflict.
tags: ["plugin", "install", "skills", "fleet"]
timestamp: 2026-09-22T17:20:00+08:00
slug: reuse-existing-plugin-skills
source: user
---

# Task: reuse existing skills during plugin install

## Goal

When `POST /api/v1/plugins/install` provisions an expert or expert team and
Fleet rejects Skill creation because that workspace already contains the exact
Skill name, bind the existing Skill and continue the install.

## Load-bearing behavior

- Reuse is limited to Fleet `409 Conflict` responses from Skill creation and an
  exact, byte-for-byte name match returned by `GET /api/skills` in the same
  caller-scoped workspace.
- Reused Skills are bound by ID without changing their content or supporting
  files.
- Rollback deletes only agents, squads, and Skills created by the current
  request; it never deletes a reused Skill.
- Members of one expert team may bind the same reused or newly created Skill ID.
- Existing legacy expert and squad install endpoints retain their current
  duplicate-name behavior.
- The `/plugins/install` request, response envelope, status mapping, and
  authentication/Space routing contract remain unchanged for both octo-web and
  OctoBuddy clients.

## Out of scope

- Comparing or merging Skill content when names match.
- Overwriting, renaming, or deleting an existing workspace Skill.
- Case-insensitive or whitespace-normalized matching.
- Changing Fleet's workspace-wide Skill uniqueness constraint.
- Changing renderer or web-client install logic.

## Acceptance criteria

- An expert plugin installs successfully when an exact-name Skill already
  exists and the created agent binds that existing Skill ID.
- An expert-team plugin binds a shared Skill ID to every member that declares
  that exact Skill name.
- A failed later step rolls back newly created resources but preserves all
  reused Skills.
- A non-conflict Fleet error, a missing exact-name match, or a legacy install
  preserves the existing error behavior.
- Relevant service, Fleet-client, handler, race, and octo-web contract tests
  pass.
