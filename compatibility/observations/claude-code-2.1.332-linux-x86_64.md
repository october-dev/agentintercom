# Claude Code 2.1.332 and OpenCode 1.19.6 on Linux x86_64

Partial, unreviewed observation from November 27, 2026. It is not compatibility evidence: both adapters remain experimental, `testedVersions` stays empty and nothing here is added to `compatibility/registry.json`. The same run is the OpenCode record; see [OpenCode 1.19.6 on Linux x86_64](opencode-1.19.6-linux-x86_64.md) for the OpenCode-specific cases.

## Setup

- **Runtime.** A development build (`agentintercom dev`, protocol 0.1) of commit `672c153`, with private `OCTOBER_BUS_DATA_DIR` and `OCTOBER_BUS_RUNTIME_DIR` and a loopback daemon. Adapters `claude-code-mcp` 0.2.0 and `opencode-mcp` 0.2.0.
- **Claude Code.** Version 2.1.332 on Ubuntu 26.04 (kernel 7.0.0, x86_64), authenticated through claude.ai, model `haiku` alias. Headless print mode: `claude -p PROMPT --model haiku --mcp-config .mcp.json --strict-mcp-config --allowedTools <twelve mcp__agent_intercom__ tools>`. The `.mcp.json` came unchanged from `agentintercom harness config claude-code --output`. No permission bypass was used. Not a clean machine.
- **Peer.** OpenCode 1.19.6 in a Debian 12 container sharing the host network and the same private data and runtime directories, with its own generated `opencode.json`. Headless `opencode run -m opencode/big-pickle`, a free model that needs no account.
- **Checks.** `doctor --harness claude-code` reported a healthy setup with 23 bridge tools. Inbox waits used `waitMs` 10000. Each print-mode turn started a new execution.

## Required scenario

Both directions completed: Claude Code as controller with OpenCode as responder and worker, then the reverse.

- Each host found the other by exact ID with `list_peers`, and each sent and acknowledged notifications.
- Each controller sent a request with an `idempotencyKey` and one text context item. Repeating it returned the original message ID and `acceptedAt`.
- Each responder acknowledged the request and replied with `responseTo`. Each controller's `message_receipt` reported the reply as `responseMessageId`. A third agent registered with `agent run` got `Message … was not found` for the same receipt.
- Each controller created a root task and a dependent task. Claiming the dependent first was refused with `… is blocked by …`. The other host claimed, released, reclaimed and completed the root, then the controller claimed and completed the dependent.
- Each host created an `ask_user` question with two options. An unauthenticated resolve returned HTTP 401. The owner answered with `agentintercom question answer`, and the asking host pulled and acknowledged the answer.
- After every turn `agentintercom sessions` showed both agents offline. Claude Code exit retired its execution without waiting for the lease.

## Additional cases

- **Rejected tool approval.** With `complete_task` left out of `--allowedTools`, Claude Code refused that call with its own permission message. The same execution then released the claimed task and listed peers.
- **Host health checks start a bridge.** With a live bridge registered as `claude-reviewer`, `claude mcp list` and `claude mcp get agent_intercom` each started a second bridge for their health check. That bridge replaced the live execution, then exited, so the agent ended offline and the original bridge reported the execution-ended message. A project server that is still pending approval is not started by `claude mcp list`.
- **Credential scan.** The current scope and admin tokens were searched for in the generated configurations, the Claude Code transcripts for the project and the captured output: 12 files, no match. Tokens rotated during the run were not part of the scan.
- **Not exercised:** the interactive terminal UI, `/mcp` reconnect, cancelling a waiting call, an unclean exit, and other platforms. The [macOS 2.1.319 interactive observation](claude-code-2.1.319-macos-arm64.md) covers those for an earlier build.

## Retries

No turn was rerun. The first `claude -p` attempt put the prompt after `--allowedTools`, which Claude Code read as a tool name; the prompt must come before that flag or after `--`.

Delivery is pull-only and process reachability does not prove model readiness. No log bundle was published; independent review is pending.
