# Claude Code 2.1.286 verification attempt

These are contributor-reported observations, not reviewed compatibility evidence. The adapter remains experimental, `testedVersions` remains empty, and this note is not included in `compatibility/registry.json`. The contributor-reported outcome is `partial`.

## Setup

- Harness: Claude Code 2.1.286, authenticated through claude.ai (first-party), model `claude-opus-5-5`.
- Launch mode: headless print mode, `claude -p` with `--setting-sources project,local --strict-mcp-config --mcp-config .mcp.json --output-format stream-json`. The `.mcp.json` file was produced by `october-bus harness config claude-code --output` and used unchanged. Each host started with a clean environment (`env -i` with only `HOME`, `USER`, `LANG`, `TERM` and `PATH`), so no inherited Bus or controller credentials reached it. Interactive terminal, IDE and desktop modes were not exercised.
- Host approvals: Claude Code 2.1.286 ignored the project `.claude/settings.json` `permissions.allow` list because the scratch workspace was not trusted. It printed `Ignoring 15 permissions.allow entries … this workspace has not been trusted`, and the first `get_node_status` call was denied and listed in `permission_denials`. Every later turn granted the 15 `october_bus` tools with `--allowedTools`. No permission bypass was used.
- Runtime and adapter: a development build (`october-bus dev`, protocol 0.1) of commit `c227136565187f605b53d53b307669a42a7b4c72` plus the uncommitted [#140](https://github.com/october-dev/october-bus/issues/140) bridge fix (`cmd/october-bus/mcp_stdio.go` diff SHA-256 `0ed87d67b25fd0b43f07c1cfe80987a6870a552f0dacd683c914150bd636a1b8`). Adapter `claude-code-mcp` 0.2.0. Private `OCTOBER_BUS_DATA_DIR` and `OCTOBER_BUS_RUNTIME_DIR`. `doctor --harness claude-code` reported a healthy setup with 15 bridge tools.
- Platform: Linux x86_64 (Fedora, kernel 7.1). Not a clean machine.
- Independent controller: Codex CLI 0.159.2, model `gpt-6.1-sol`, isolated `CODEX_HOME`. It used the generated Codex config with `default_tools_approval_mode` changed from `prompt` to `approve`, because `codex exec` cannot answer approval prompts.

Each agent turn was a separate headless session, so the bridge registered a new execution per turn. Claims that had to persist were exercised within a single turn.

## Required scenario

Steps 1-11 and 13 of the [runbook](../RUNBOOK.md) completed in both directions: Claude Code as controller with Codex as responder/worker, then Codex as controller with Claude Code as responder/worker. Step 12 is covered by the duplicate-session case below.

- Discovery by exact ID, durable notifications with acknowledgement, and requests with bounded `text` context all worked. Retrying a request with the same `idempotencyKey` returned the original message ID.
- `message_receipt` reported the linked `responseMessageId` in both directions. An unrelated agent, registered by the owner, got `NOT_FOUND` for the same receipt.
- A dependent task could not be claimed while blocked (`… is blocked by …`). Claim, release, reclaim and complete all worked, and the dependent task then became claimable and was completed by the other agent.
- `ask_user` created a pending escalation in both directions. An unauthenticated resolve returned `UNAUTHENTICATED`, and resolving with an agent credential returned `PERMISSION_DENIED`. The scope owner then resolved it over HTTP.
- Clean exit left both agents offline and unreachable, with no claims held.
- Unclean exit: with a claim held and a `check_inbox` wait pending, the bridge and then Claude Code were sent `SIGKILL`. The execution stayed ready 10 seconds later. By 36 seconds its 30-second lease had expired: it was offline and the claim was released.
- Separately, killing Claude Code alone (bridge still alive) retired the execution at once: the bridge saw stdin close and released the claim.
- Claude Code passed genuine JSON arrays for `messageIds`, `dependencies` and `context`; no stringified arrays were observed.

## Additional cases

- **Duplicate-session replacement.** Session 1 registered `claude-reviewer` and started a 25-second `check_inbox` wait. Session 2 then registered the same ID. Session 1's pending wait failed with `Invalid agent token`, and its next four calls each returned the fixed error `October Bus execution ended. This server will not reconnect by itself. …`. Session 2's wait, `list_peers` and `get_node_status` all succeeded, and its lease kept renewing. Owner snapshots every 3 seconds showed session 2's `executionId` from 09:25:03 until session 2 exited cleanly; session 1 never registered again. The newer session kept the ID.
- **Scope token rotation.** `scope rotate-token` ran during a session's 25-second wait. The wait failed with `Agent execution lease has expired`, and every later call returned the fixed terminal error. Owner snapshots with the new token, taken every 3 seconds for 48 seconds, showed only the original, now offline, execution, so no new execution appeared. A fresh session then connected with the refreshed local credential and its calls succeeded.
- **Rejected tool approval.** With `complete_task` left out of `--allowedTools`, Claude Code denied that call (`permission_denials`). `release_task` and `list_peers` on the same bridge then succeeded.
- **Missing local scope token.** With a scope's local token file removed, the host reported the `october_bus` server as `failed` at startup rather than showing a tool-less server. Run directly, the bridge printed `cannot read local scope credential; create the scope locally or rotate its token`.
- **Credential scan.** The scope tokens (original, rotated and the missing-token scope), the admin token, and an owner-registered agent token were searched for in 46 files: the generated configurations (which hold the bridge arguments), the host settings, and every transcript and stderr file. None was found. The only other token-shaped strings were fragments of model signature blobs.
- **Not exercised in print mode:** cancelling a waiting tool call, and reconnecting from `/mcp` or reloading the window after an ended session.

## Retries and failures

No turn was rerun. Two model-side input corrections were caused by the operator's prompts. Claude Code was told to use message mode `notification`, got `mode is invalid`, and retried with `notify`. In one unclean-exit attempt it was told `waitMs` 30000 and got `waitMs must be between 0 and 25000`. That first unclean-exit attempt was also invalid for a different reason: the operator's kill command terminated its own shell before reaching the bridge. The bridge therefore retired on stdin close, which is the clean-exit case above, so the lease-expiry case was repeated with a corrected kill script. One Claude Code turn was denied by workspace trust, as described under setup.

## Artifacts

A local, unreviewed bundle was produced with `scripts/verification-bundle.mjs`. It contains the stream-json transcripts of every Claude Code turn, the Codex `--json` event streams, host stderr, the generated configurations and launch scripts, and the owner-side checks, with home paths and known tokens redacted. The sanitized log digest is `sha256:6ed0641779a3581290a297efd8ee85e1b318968b4f980814649c57b87614fd2f` (359426 bytes). The bundle is not published. It is available for maintainer review under the [maintainer-assisted workflow](../VERIFICATION.md), and the digest is recorded here only for provenance.

Delivery is pull-only and process reachability does not prove model readiness. [Issue #37](https://github.com/october-dev/october-bus/issues/37) remains open for interactive mode, interactive cancellation and `/mcp` reconnect or reload, a clean-machine run, and independent review.
