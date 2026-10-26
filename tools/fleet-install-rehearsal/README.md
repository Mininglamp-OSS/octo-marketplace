# Isolated Fleet / Daemon installation rehearsal

This runs Marketplace's actual `PreparePinnedSkills`, Fleet's unmodified
`CreateCapabilityInstallation` against real PostgreSQL, and Daemon's unmodified
`writeSkillFiles`. It adds temporary tests with Go overlays; it does not edit the
Fleet or Daemon checkout or change their protocols.

Provide an **empty disposable local PostgreSQL database** named
`marketplace_rehearsal`. The script runs Fleet migrations and Fleet's test fixture
setup/cleanup. It refuses a remote host, a missing explicit port, privileged
ports, and any other database name. Never provide a real environment's database.

```bash
# Supply credentials through the process environment; do not commit them.
export DATABASE_URL='<isolated localhost PostgreSQL URL>'
./tools/fleet-install-rehearsal/run.sh \
  /absolute/path/to/octo-fleet \
  /absolute/path/to/octo-daemon \
  /absolute/path/to/temporary-output
```

The selected sources need the Go versions in their respective `go.mod` files.
The output directory contains synthetic request/stored files, migration/test logs
and `result.json` with SHA-256 fingerprints of the exact tested source trees
and their Git commit IDs when the sources are actual Git checkouts. An archive
never inherits an enclosing repository’s commit. Before/after fingerprints must
match, and Go dependencies are read-only. Output permissions
are restricted. There is no database URL or credential in the result report.

Verified cases:

- Three skills with the market display name `Search`: two resource IDs and two
  versions of one ID receive three stable, distinct runtime names.
- The actual Fleet handler writes three skills and their supporting files,
  installs two experts with exactly their four declared bindings, and creates a
  two-member expert team. Same-key replay returns the same team.
- Actual Daemon materialization produces three separate directories whose
  frontmatter names agree with Fleet, preserving body and supporting files.

This verifies the installation database and materialization boundaries. It does
**not** run an online daemon dispatch, provider-native skill discovery, an LLM
conversation, a real MCP server, or a complete Loop execution. No LLM service or
model is started and no feature flag is enabled. I4's remaining online/runtime
acceptance must be reported separately rather than inferred from this rehearsal.
