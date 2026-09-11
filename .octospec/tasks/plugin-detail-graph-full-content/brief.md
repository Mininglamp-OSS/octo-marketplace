---
type: Task
title: "Plugin: return installable content from detail_graph"
description: Make every related plugin in `GET /plugins/detail_graph` use the full plugin projection so desktop clients can resolve an expert or expert-team dependency graph in one request.
tags: ["plugin", "api", "client"]
timestamp: 2026-09-11T17:10:00+08:00
slug: plugin-detail-graph-full-content
source: user
---

# Plugin: return installable content from detail_graph

## Goal

Change `GET /plugins/detail_graph` so `related_plugins` includes the same full
plugin projection as the root, including `plugin_json`. This lets a desktop
client obtain team instructions, member instructions, and related Skill package
metadata without issuing one `/plugins/detail` request per graph node.

## Load-bearing behavior

- Keep the existing URL, authentication, response envelope, flat graph shape,
  relation ordering, deduplication, and fixed traversal depth.
- Preserve caller/Space visibility checks independently for every related node.
- Preserve the existing node and edge limits and fail closed rather than return
  a partial graph.
- Cap aggregate manifest, package, and relation JSON at 100 MiB and include
  `max_bytes` in the existing 413 details.
- Return the full `pluginResponse` fields for every item in `related_plugins`,
  including `plugin_json`; retain the existing optional `member_count` field
  for wire compatibility and never expose the host-private
  `attachment_keys_json` sidecar.
- Keep ordinary marketplace list responses lightweight and unchanged.
- Regenerate OpenAPI and run both `make openapi-check` and `make openapi-diff`.

## Out of scope

- Client integration and local installation behavior.
- Changing `/plugins/detail`, `/plugins/download`, graph traversal depth, or
  relation visibility rules.
- Inlining storage-backed Skill files into JSON; clients continue to use the
  authenticated download endpoint for archive bytes when required.

## Acceptance criteria

- A team graph returns full `plugin_json` for its member experts and Skills.
- A direct expert graph returns full `plugin_json` for its Skills.
- Cross-Space and hidden descendants remain absent.
- Repository, handler, service, OpenAPI, and full Go tests pass.
