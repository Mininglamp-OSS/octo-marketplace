---
type: Task
title: "Task: skip duplicate squad skills"
description: Prevent squad installation rollback when members package Skills with duplicate names.
tags: ["expert", "squad", "skills", "installation"]
timestamp: 2026-08-18T15:34:00+08:00
slug: skip-duplicate-squad-skills
upstream: Mininglamp-OSS/octo-marketplace#62
source: self
---

# Task: skip duplicate squad skills

## Goal

Allow an expert squad to install into a Loop workspace when multiple members
package Skills with the same name. During one squad installation, create and bind
the first packaged Skill with a normalized name and skip later duplicates.

## Background

Squad installation provisions every member into the same Loop workspace. Fleet
uses workspace-wide Skill names, so creating a later member's same-named Skill
returns a conflict and causes the marketplace's atomic squad installation to roll
back. See [issue #62](https://github.com/Mininglamp-OSS/octo-marketplace/issues/62).

## Load-bearing list

- The squad install sequence: provision all member agents, create the Loop squad,
  and add non-leader members.
- Per-member Skill creation and binding through Fleet.
- Atomic rollback of Skills, agents, and the squad after any provisioning error.
- Standalone expert installation, which shares the agent/Skill provisioning path.
- Skill package file fan-out accounting across the complete squad installation.

## Out of scope

- Reusing the first Skill ID by binding it to later members.
- Comparing duplicate Skill contents or package digests.
- Detecting Skills that already existed in the target workspace before this
  installation began.
- Changing Fleet's workspace-wide Skill name uniqueness contract.
- Changing standalone expert installation semantics.

## Acceptance

- The first packaged Skill for a normalized name is created and bound normally.
- Later packaged Skills with that name are not created or bound.
- Name normalization trims surrounding whitespace and ignores letter case.
- Unique Skills on later members still install and bind normally.
- Existing rollback behavior remains intact.
- `go test ./internal/service/expert` passes.
- `go test ./...` passes.
