# Claude Code 2.1.282 verification attempt

These are contributor-reported observations, not reviewed compatibility evidence. The adapter remains experimental, `testedVersions` remains empty, and this note is not included in `compatibility/registry.json`. The contributor-reported outcome is `partial`.

## Setup

- Harness: Claude Code 2.1.282, authenticated through claude.ai (first-party), model `claude-sonnet-5`.
- Launch mode: headless print mode, `claude -p` with `--setting-sources project,local --strict-mcp-config --mcp-config .mcp.json`. The `.mcp.json` file was produced by `agentintercom harness config claude-code`. The flags excluded the operator's unrelated user-level MCP servers and hooks. Interactive terminal, IDE and desktop modes were not exercised.
- Host approvals: a project-local `permissions.allow` list naming the 15 `october_bus` tools. No permission bypass was used.
- Runtime: the released `agentintercom_0.1.0-rc.5_darwin_arm64` archive, checksum verified against the release's `checksums.txt`, tag commit `8357265c1113b6c7f87fb11d87924ee122e97156`. Private `OCTOBER_BUS_DATA_DIR` and `OCTOBER_BUS_RUNTIME_DIR`.
- Adapter: `claude-code-mcp` 0.2.0, protocol 0.1, macOS arm64. Not a clean machine.
- Independent controller: Codex CLI 0.153.4 (the ChatGPT app bundle), model `gpt-5.6-terra`, isolated `CODEX_HOME`. It used the generated Codex config with `default_tools_approval_mode` changed from `prompt` to `approve`, because `codex exec` cannot answer approval prompts.

Each agent turn was a separate headless session, so the bridge registered a new execution per turn. Claims were exercised within a single turn.

## Required scenario

Steps 1-13 of the [runbook](../RUNBOOK.md) completed in both directions: Claude Code as responder/worker with Codex as controller, then Claude Code as controller with Codex as responder/worker.

- Discovery by exact ID, durable notifications with acknowledgement, and request/response with bounded context all worked. A retried request with the same `idempotencyKey` returned the original message ID.
- `message_receipt` reported the linked `responseMessageId`. An unrelated agent's lookup failed with `Message … was not found`.
- A dependent task could not be claimed while blocked. Claim, release, reclaim, progress and complete all worked, and the dependent task then became claimable.
- `ask_user` created a pending escalation. An unauthenticated resolve returned `UNAUTHENTICATED`, and the scope owner resolved it over HTTP.
- Clean exit left both agents offline and unreachable, with no claims held.
- After `SIGKILL` of both Claude Code and its bridge, the execution stayed ready until its 30-second lease expired. It then went offline, and its claim was released.
- Claude Code passed genuine JSON arrays for `messageIds`, `dependencies`, `options` and `context`; no stringified arrays were observed.

## Additional cases

- **Rejected tool approval.** Removing `complete_task` from the allowlist made Claude Code deny the call (reported in `permission_denials`). Later calls on the same bridge succeeded. `--disallowedTools` instead hides the tool from the model entirely.
- **Missing local scope token.** The host reported the `october_bus` server as `failed` at startup, rather than showing a tool-less server.
- **Scope token rotation.** `scope rotate-token` revoked the live execution, and its next call failed with `Unauthorized`. Claude Code then restarted the stdio server, which re-registered with the refreshed local token. A fresh session connected normally.
- **Duplicate-window replacement.** A second session with the same agent ID replaced the first. The first session's next call failed with `Invalid agent token`, and its task claim was released. However, Claude Code restarted the replaced stdio server, which self-registered again and revoked the newer session (`Unauthorized`). With Claude Code, the older window ends up holding the agent ID, rather than the newer one replacing it.
- **Not exercised in print mode:** cancelling a waiting tool call, and interactive host restart/reload.

## Retries and failures

One controller-side retry: a Codex turn registered its bridge but loaded no MCP tools and made no calls. It was rerun unchanged. No Claude Code turn needed a retry or a corrected tool input.

## Artifacts

A local, unreviewed bundle was produced with `scripts/verification-bundle.mjs`. It contains the full stream-json transcripts of every turn and the owner-side checks, with home and memory paths redacted. The sanitized log digest is `sha256:13c3d8212cb4b99b5b4da7861595ad783537c8feb1777fe8bca19dc024cc188a` (652770 bytes). A scan found no scope or admin token in any configuration file, argument or transcript. The bundle is not published. It is available for maintainer review under the [maintainer-assisted workflow](../VERIFICATION.md), and the digest is recorded here only for provenance.

Delivery is pull-only and process reachability does not prove model readiness. [Issue #37](https://github.com/october-dev/october-bus/issues/37) remains open for interactive-mode runs, the cancellation case, a clean-machine run, and independent review.
