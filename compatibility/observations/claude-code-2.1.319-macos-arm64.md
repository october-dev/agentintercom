# Claude Code 2.1.319 interactive verification attempt

These are contributor-reported observations, not reviewed compatibility evidence. The adapter remains experimental, `testedVersions` remains empty, and this note is not included in `compatibility/registry.json`. The contributor-reported outcome is `partial`.

This attempt targets the gaps the earlier [2.1.282](claude-code-2.1.282-macos-arm64.md) and [2.1.286](claude-code-2.1.286-linux-x86_64.md) print-mode attempts left open: the interactive terminal UI, cancelling a waiting tool call, `/mcp` reconnect and window restart, and duplicate-window replacement with the [#140](https://github.com/october-dev/agentintercom/issues/140) fix ([#146](https://github.com/october-dev/agentintercom/pull/146)).

## Setup

- Harness: Claude Code 2.1.319, authenticated through claude.ai (Claude Max), model Opus 5.5 (`claude-opus-5-5`), effort medium.
- Launch mode: interactive terminal UI (`config-only`), driven through tmux 3.8: `claude --setting-sources project,local --strict-mcp-config --mcp-config .mcp.json`. The `.mcp.json` file was produced by `agentintercom harness config claude-code --output` and used unchanged. Each window started with a clean environment (`env -i` with only `HOME`, `USER`, `LANG`, `TERM` and `PATH`). IDE and desktop modes were not exercised.
- Host approvals: the first launch showed the workspace trust prompt, which was accepted. Claude Code 2.1.319 starts in **auto mode**, now its default permission mode: each tool call is checked and approved or blocked automatically, without a prompt. Auto mode approved every `agent_intercom` call in this run and blocked none. The rejected-approval case used manual mode (Shift+Tab). No permission bypass and no `permissions.allow` list were used.
- MCP surface: `/mcp` listed `agent_intercom` as connected with **23 tools**. Claude Code's built-in `claude-in-chrome` and `computer-use` servers were also listed despite `--strict-mcp-config`; they were not used. No `MCP_TIMEOUT` or `MCP_TOOL_TIMEOUT` was set, so Claude Code's default startup and tool-call timeouts applied.
- Runtime and adapter: a development build (`agentintercom dev`, protocol 0.1) of `main` at `496d0d85b5febec72837971e55867d5aea7fcef2`, built with Go 1.27.3. No release yet contains #146; rc.5 predates it. Adapter `claude-code-mcp` 0.2.0. Private `OCTOBER_BUS_DATA_DIR` and `OCTOBER_BUS_RUNTIME_DIR`. `doctor --harness claude-code` reported a healthy setup with 23 bridge tools.
- Platform: macOS 27.0.1, arm64. Not a clean machine.
- Independent controller: Codex CLI 0.167.0, model `gpt-5.6-terra`, isolated `CODEX_HOME`, using the generated Codex config with `default_tools_approval_mode` changed from `prompt` to `approve`, because `codex exec` cannot answer approval prompts. Each Codex turn was a separate headless session, so its bridge registered a new execution per turn.

Claude Code kept one interactive window, and so one execution, across turns until a case deliberately replaced or ended it.

## Required scenario

Steps 1-13 of the [runbook](../RUNBOOK.md) completed in both directions: interactive Claude Code (`agent-b`) as responder/worker with Codex (`agent-a`) as controller, then Claude Code as controller with Codex as responder/worker.

- Discovery by exact ID and durable notifications with acknowledgement worked both ways. A request with bounded `text` context, retried with the same `idempotencyKey`, returned the original message ID in both directions.
- `message_receipt` reported the linked `responseMessageId` in both directions. An unrelated agent registered by the scope owner got `NOT_FOUND` for the same receipt.
- A dependent task could not be claimed while blocked (`… is blocked by …`). Claim, release, reclaim, progress and complete all worked, and the dependent task then became claimable and was completed by the other agent.
- `ask_user` created a pending escalation. An unauthenticated resolve returned `UNAUTHENTICATED`; the scope owner resolved it over HTTP, and the answer reached Claude Code's next `check_inbox` as a notification from `owner`.
- Restart: `/exit` and relaunch registered a new execution.
- Clean exit: `/exit` (status 0) while holding a claim left the agent offline and unreachable, and the claim was released.
- Unclean exit: with a claim held and a 25-second `check_inbox` in flight, the bridge and Claude Code were sent `SIGKILL`. The execution stayed ready for about 25 seconds, went offline when its 30-second lease expired, and the claim was released.
- Claude Code passed genuine JSON arrays for `messageIds`, `dependencies`, `options` and `context`; no stringified arrays were observed.

## Interactive cases

- **Cancelling a waiting tool call.** During a 20-second `check_inbox`, Esc showed `Interrupted`. The next call reported the same execution, still ready with a renewed lease. A notification sent after the cancellation was returned by the next `check_inbox`, so the cancelled wait did not consume it. The first attempt did not exercise cancellation, because a pending message made the wait return at once; it was repeated with an empty inbox.
- **Rejected tool approval.** In manual mode, choosing "No" at the `list_peers` approval prompt ended the turn (`Interrupted`). The next call, approved, succeeded on the same execution.
- **Duplicate-window replacement.** Opening a second interactive window with the same configuration registered a new execution at startup, before any tool call, replacing the first. The first window's next call returned the fixed error `Agent Intercom execution ended. This server will not reconnect by itself. …`. Owner snapshots every 5 seconds for 40 seconds showed the newer execution throughout: the first window never registered again. This is the opposite of the 2.1.282 result, where Claude Code's restart of the stdio server took the ID back.
- **`/mcp` reconnect.** While the ended first window's tools all failed, `/mcp` still showed `agent_intercom` as connected with 23 tools. Choosing Reconnect registered a new execution, and the second window's next call then returned the same fixed error. An explicit reconnect is therefore how a user moves an agent ID between windows.
- **Scope token rotation.** `scope rotate-token` ended the live interactive execution: its next call returned the fixed error, and owner snapshots for 30 seconds showed it offline with no new execution. `/mcp` Reconnect then registered a new execution using the refreshed local credential.
- **Missing local scope token.** With the scope's local token file moved away, a new window listed `agent_intercom` as `✘ failed` in `/mcp`, offering only Reconnect and Disable, rather than a server without tools.
- **Credential scan.** The original and rotated scope tokens were searched for in 24 files: the generated configurations, the tmux transcripts, the Codex event streams and stderr, and Claude Code's session transcripts. None was found.

## Retries and failures

- The fixed terminal error tells the user to "Resolve any duplicate agent ID" whatever ended the execution. After the token rotation, Claude Code repeated that advice and suggested looking for another session with the same ID, although there was none.
- After a successful claim, Claude Code called the task's `ready: false` a "possible bug". It is specified behavior: a task is ready only while it is open, unclaimed and unblocked ([spec](../../spec/0.1/README.md)).
- Codex once called `acknowledge_messages` with an empty `messageIds` array, then repeated the call with the received ID in the same turn.
- Codex CLI 0.136.0 could not run the controller: the account rejected its default model, and `gpt-5.6-terra` required a newer CLI. Codex 0.167.0 was installed in a private prefix for this run.
- No Claude Code turn was rerun, and Claude Code needed no corrected tool input.

## Artifacts

A local, unreviewed bundle was produced with `scripts/verification-bundle.mjs`. It contains the operator's owner-side checks, the tmux transcripts of every interactive window, the `agent_intercom` tool calls and results extracted from Claude Code's session transcripts, and the Codex `--json` event streams. Claude Code's full session transcripts were not included because they carry the operator's private context. The sanitized log digest is `sha256:472d87ac25b5a3c50502ee5291b53d218c4e8c69841414b0b89e758c59db433d` (192730 bytes). The bundle is not published. It is available for maintainer review under the [maintainer-assisted workflow](../VERIFICATION.md), and the digest is recorded here only for provenance.

Delivery is pull-only and process reachability does not prove model readiness. [Issue #37](https://github.com/october-dev/agentintercom/issues/37) remains open for a released runtime that includes #146, a clean-machine run, IDE and desktop modes, and independent review.
