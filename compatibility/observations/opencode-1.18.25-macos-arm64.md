# OpenCode verification attempt

These are contributor-reported observations from [PR #98](https://github.com/october-dev/october-bus/pull/98), not reviewed compatibility evidence. The adapter remains experimental and this note is not included in `compatibility/registry.json`.

The contributor reports attempting the RUNBOOK with OpenCode 1.18.25 through headless `opencode run` on macOS arm64. In that attempt, `acknowledge_messages` reportedly received `messageIds` as a JSON string instead of an array and failed. The source of that argument shape has not been independently established; it must not yet be attributed to the OpenCode MCP client. Bus-side observations alone do not establish successful harness-side acknowledgement.

The runtime version is unresolved: the PR described a released runtime, while its supplied metadata said `dev`. That metadata identified adapter `opencode-mcp` 0.1.0, protocol 0.1, time `2026-09-03T16:36:59Z`, and repository commit `ac6c380b897b4d5bb1a8dcd61e621ac6c0c43870`.

The earlier digest, `sha256:b01fdc518621da79321dac9bde6be7857f7cc367700b4892971c5fb4b42871a2`, is retained only for provenance. No public log matching the named-harness attempt accompanies this note; neither that digest nor the generic bridge conformance command establishes the reported outcome.

Delivery is pull-only and process reachability does not prove model readiness. Interactive approval prompts, other platforms, and other versions remain unverified. [Issue #34](https://github.com/october-dev/october-bus/issues/34) remains open for a reproducible log with exact versions, investigation of the acknowledgement failure, and independent [runbook](../RUNBOOK.md) review.
