---
type: Task
title: "Task: project-scaffold"
description: Create a runnable independent Go service scaffold based on octo-smart-summary.
tags: ["architecture"]
timestamp: 2026-07-14T00:00:00+08:00
slug: project-scaffold
source: self
---

# Task: project-scaffold

## Goal

Create a buildable API service skeleton for future Skill and MCP market
work, following the deployment shape of `octo-smart-summary`.

## Load-bearing list

- Independent service boundary.
- Public health and MySQL-backed readiness endpoints.
- MySQL connection boundary and reserved Octo auth integration.

## Out of scope

- Marketplace catalog, publishing, policy, artifact, installation, and CLI APIs.
- Web and `octo-cli` integration.
- Role and Agent ownership resolution.

## Acceptance

- `go test ./...` passes.
- `go build ./...` passes.
- `docker compose up --build` starts the API.
- `/healthz` and MySQL-backed `/readyz` return successfully.
