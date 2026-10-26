# Offline installation primitives (I preparation)

This unit adds pure Go functions in `internal/service/plugin/install_primitives.go`.
No HTTP entrypoint uses them yet; no database, Fleet request, feature flag or MCP
process is involved. Inputs must already have been authorized and verified by the
future pinned-version resolver. This is not the complete I1/I2 installation chain.

## Executable fixture cases

```sh
export PATH=/home/mlamp/go-sdk/go/bin:$PATH
go test ./internal/service/plugin -run 'TestPreparePinnedSkills|TestInstallFrontmatter|TestMergeCurrentConnector' -count=1 -v
```

`install_primitives_test.go` constructs fixed, secret-free inputs: two skills named
Search from different IDs, another version of one ID, and two current connectors.
Tests run the actual functions and check their outputs, without external services.

## Skill identity and temporary copy

`PreparePinnedSkills([]PinnedSkillFiles)` takes plugin ID, immutable version ID,
name/description from that snapshot, and root-relative UTF-8 text files. A version
must always have identical content. Duplicate references to identical snapshots
reuse one result; conflicting content under one identity fails.

Name = ASCII slug (up to 24 bytes, fallback `skill`) + `-mk1-` + first 128 bits
of SHA-256(JSON array `["mk1", plugin_id, version_id]`), encoded as 32 hex digits.
The maximum is 61 bytes. The result is stable for the same immutable input;
different resource IDs/versions have different suffixes, with a final collision
check. The display name in Marketplace is not changed. Do not pass a mutable
current display name with a historical version ID.

The returned Fleet skill name, `Directory` and SKILL.md YAML name are identical.
Only YAML frontmatter is rewritten or added. Markdown body and companion files
are unchanged, preserving relative references. Hard-coded old skill names and
absolute paths are compatibility issues, not blindly replaced. YAML anchors,
malformed/duplicate metadata, binary content, traversal and reserved paths fail.
Input maps are never mutated. No filesystem directories are created here;
`Directory` identifies the name downstream materialization must use.

Limits reuse portable-install constraints: 600 input references, 51 files per
skill including SKILL.md, 500 companion files total, 1 MiB per text file and
16 MiB aggregate text budget. The final composed Fleet request must still pass
its existing validation/serialized-payload budget, since JSON escaping and other
expert fields add bytes. This helper alone cannot certify the whole request.

## MCP current configuration merge

`MergeCurrentConnectorMCP([]CurrentConnectorMCP)` takes connector IDs and their
already-authorized **current effective** configuration, not expert-package MCP
files or connector versions. Fetch each ID once per resolution. This function
does not read storage or decide which configuration has completed approval.

Repeated same ID + equal normalized configuration is deduplicated. Same ID +
different configuration fails, preventing an internally inconsistent resolution.
Different IDs with the same server key fail even if their contents match: neither
overwrite nor implicit rename is allowed. Separate calls with an updated current
configuration produce updated output. Object output is deterministic.

Existing portable MCP validation runs on each input and on the merged result:
transport/allowed fields, 100 servers and configured byte limit apply. Raw input
has a 16 MiB aggregate budget and 600-reference cap. Command configuration omits
`type`; explicit `stdio` stays rejected by the existing Fleet contract. Errors
contain fixed reasons, never configuration values. This transient helper does not
persist or log credentials, authenticate MCP, or test reachability.

## Remaining integration

I1 must resolve authorized fixed snapshots, verify artifacts and release evidence,
and map prepared results back to expert bindings. I2 must load effective connector
configs by ID. I3 must connect both existing entrypoints, validate the complete
payload and implement retries/compensation. I4 must verify real Fleet/Daemon
installation and discovery before rollout.

Local SDK note: the installed Go SDK detects Git roots using directory `.git`
only, skipping worktree `.git` files and selecting `/home/mlamp/.git`. In that
specific environment use `GOFLAGS=-buildvcs=false go build ./...`; this disables
only binary VCS stamping, not compilation or tests. Do not change repository or
parent `.git` metadata to work around it.
