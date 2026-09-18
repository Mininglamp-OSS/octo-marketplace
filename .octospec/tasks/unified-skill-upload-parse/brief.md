---
type: Task
title: "Task: decouple unified Skill parsing from the legacy catalog"
description: Stop the shared upload parser from consulting the retired skills table.
tags: ["plugin", "skill", "upload", "migration"]
timestamp: 2026-09-18T14:15:00+08:00
slug: unified-skill-upload-parse
source: self
---

# Task: decouple unified Skill parsing from the legacy catalog

## Goal

Make ZIP parsing a content-only operation so unified Plugin Skill upgrades are
not rejected by stale rows in the retired `skills` catalog.

## Load-bearing behavior

- Parsing validates archive safety and `SKILL.md` syntax/content only.
- Parsing never reads the legacy `skills` table.
- Unified Plugin import and review remain responsible for binding a successful
  parse task to a target Plugin and validating its declared name.
- Parse-task ownership, Space isolation, status, and single-consumption checks
  remain unchanged.

## Out of scope

- Dropping legacy database tables or migrations.
- Removing legacy HTTP routes in this patch.
- Changing the unified Plugin naming policy.
- Changing upload, archive, or review wire contracts.

## Acceptance criteria

- A valid unbound Skill upload parses successfully without querying `skills`,
  including when a retired catalog row would have the same package name.
- Invalid ZIP/frontmatter inputs continue to fail at the parsing boundary.
- Unified review still rejects a package whose declared name differs from the
  target Plugin.
- Relevant package tests, the full Go test suite, and OpenAPI validation pass.
