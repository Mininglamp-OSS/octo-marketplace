# Offline installation primitives

Goal: prepare immutable skill snapshots for the existing Fleet inline-text contract and merge already-authorized current connector configurations.

Load-bearing: stable resource/version install identity; matching runtime directory/frontmatter; preservation of original files and Markdown body; text/path/count/size validation; deterministic MCP conflict rejection without secret-bearing errors.

Out of scope: database resolution, authorization, latest-version fallback, HTTP handlers, Fleet protocol changes, production flags, installation retries and migration. Callers must load and verify authorized immutable skill artifacts and effective connector configurations. No live entrypoint calls these functions yet.

Acceptance: same identity gives stable output; distinct IDs/versions coexist; conflicting contents for the same identity fail; malformed YAML/unsafe paths/binary data fail; source files and body remain unchanged; MCP duplicates, unsupported transport and aggregate budgets fail. Narrow and full Go tests pass.
