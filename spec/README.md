# Agent Intercom protocol

This directory contains the public Agent Intercom protocol specification.

| Version | Status | Documents |
| --- | --- | --- |
| 0.1 | Draft | [Overview](0.1/README.md) · [HTTP API](0.1/http.md) · [MCP mapping](0.1/mcp.md) · [Adapter contract](0.1/adapters.md) |

Protocol versions are separate from runtime and SDK versions. A runtime reports its protocol version from `GET /health`.

During pre-stable development, evolve the draft `0.1` tree in place, documenting migration requirements in each release. Git history preserves previous drafts; do not maintain competing draft trees. At the explicit stable freeze, review the entire contract and rename the tree once to `1.0`. After that freeze, breaking changes require a new protocol version and preservation of the previous stable specification.

Before stable, pair runtime/CLI and SDK from the same reviewed release. Health feature checks and SQLite schema refusal detect specific incompatibilities; they do **not** enforce exact package-version equality or establish mixed-version support.

## Language

The words MUST, MUST NOT, SHOULD, SHOULD NOT, and MAY describe protocol requirements.

## Changes

Protocol changes should include:

1. the specification update;
2. matching JSON Schema changes;
3. reference-runtime tests;
4. conformance tests;
5. migration notes when existing clients are affected.

New extension or negotiation mechanisms should be added only after real integrations demonstrate the need.
