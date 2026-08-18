---
type: Task
title: "Task: skip duplicate squad skills"
description: Prevent squad installation rollback when members package Skills with duplicate names.
tags: ["expert", "squad", "skills", "installation"]
timestamp: 2026-08-18T15:34:00+08:00
slug: skip-duplicate-squad-skills
upstream: Mininglamp-OSS/octo-marketplace#62, Mininglamp-OSS/octo-marketplace#64
source: self
---

# Task: skip duplicate squad skills

## Goal

Allow an expert squad to install into a Loop workspace when multiple members
package Skills with the same name, while preserving its squad-level dispatch
instructions. During one installation, create and bind the first packaged Skill
with an exact stored name, skip later byte-identical duplicates, and write numbered strategies
through Fleet's existing squad-update route after creation.

## Background

Squad installation provisions every member into the same Loop workspace. Fleet
uses workspace-wide Skill names, so creating a later member's same-named Skill
returns a conflict and causes the marketplace's atomic squad installation to roll
back. See [issue #62](https://github.com/Mininglamp-OSS/octo-marketplace/issues/62).

## Load-bearing list

- The squad install sequence: provision all member agents, create the Loop squad,
  update it with rendered instructions, and add non-leader members.
- The deployed Fleet `PUT /api/squads/{id}` partial-update contract, which accepts
  an `instructions`-only body without clobbering omitted fields.
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

- The first packaged Skill for an exact stored name is created and bound normally.
- Later packaged Skills with that byte-identical name are not created or bound.
- Case and whitespace variants remain distinct and are all installed.
- Unique Skills on later members still install and bind normally.
- Non-blank strategies are trimmed and rendered as ordered numbered lines in an
  `instructions`-only Fleet squad update; no strategies skip that update.
- Duplicate-Skill filtering and instruction rendering work in the same install.
- A transient instruction-update failure is retried with a bounded policy before
  destructive rollback.
- A persistent Fleet create or instruction-update failure rolls back the squad
  when created, all member agents, and created Skills.
- Existing rollback behavior remains intact.
- `go test ./internal/service/expert` passes.
- `go test ./...` passes.
