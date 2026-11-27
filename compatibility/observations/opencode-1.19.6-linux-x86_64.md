# OpenCode 1.19.6 on Linux x86_64

Partial, unreviewed observation from November 27, 2026. It is not compatibility evidence: the adapter remains experimental, `testedVersions` stays empty and nothing here is added to `compatibility/registry.json`.

## Setup

- **Host.** OpenCode 1.19.6 installed with `npm install -g opencode-ai` in a Debian 12 container (x86_64). Headless `opencode run -m opencode/big-pickle`, a free model that needs no account. The `opencode.json` came unchanged from `agentintercom harness config opencode --output`, with no `permission` block, so OpenCode's default allowed the tools.
- **Runtime.** A development build (`agentintercom dev`, protocol 0.1) of commit `af89cad` for the single-host cases and `672c153` for the two-host run and the replacement case. Adapter `opencode-mcp` 0.2.0, private data and runtime directories, loopback daemon.
- **Checks.** `opencode mcp list` showed `agent_intercom` connected. `doctor --harness opencode` reported a healthy setup with 23 bridge tools.

## Two-host run

The full scenario ran in both directions with Claude Code 2.1.332 as the independent host. That run is recorded in [Claude Code 2.1.332 and OpenCode 1.19.6 on Linux x86_64](claude-code-2.1.332-linux-x86_64.md): discovery, notifications, idempotent requests with context, linked replies and receipts, blocked and dependent tasks, claim/release/reclaim/complete, and an owner-answered `ask_user` question. A separate single-host run with a shell peer (`agent run` and the message and task CLI commands) repeated discovery, request and reply, a shared task and an answered question.

OpenCode passed genuine JSON arrays for `messageIds` and `dependencies` in every call. The stringified array reported in the [1.18.25 observation](opencode-1.18.25-macos-arm64.md) did not occur. The model once sent the string `"null"` as an optional task description, which the task stored as text.

## Additional cases

- **Clean exit and kill.** After each `opencode run` the agent was offline at once. Killing the OpenCode process with `SIGKILL` during a 25-second wait closed the bridge's input, and the execution was offline within two seconds.
- **Duplicate agent ID.** During a 25-second `check_inbox` wait, a second bridge registered the same ID. On the `af89cad` build the pending wait failed with `Invalid agent token` and the next call with `MCP error 0: … Unauthorized`. On `672c153` both calls returned the execution-ended message. The newer execution kept the ID.
- **Scope token rotation.** Rotating the token during a wait ended the execution with the same message. The next `opencode run` registered with the refreshed local credential.
- **Host health checks start a bridge.** With a live bridge registered as `opencode-reviewer`, `opencode mcp list` started a second bridge that replaced the live execution and then exited, leaving the agent offline.
- **Tool permissions.** A `permission` block of `"agent_intercom_*": "deny"` with `"agent_intercom_get_node_status": "allow"` hid every other tool from the model. With `"agent_intercom_*": "ask"`, `opencode run` printed `permission requested … auto-rejecting` and the call failed with a rejection.
- **Credential scan.** The current scope and admin tokens were searched for in the project and in OpenCode's data, state and cache directories: 45 files, no match. Tokens rotated during the run were not part of the scan.
- **Not exercised:** the interactive TUI, other platforms and other models.

Delivery is pull-only and process reachability does not prove model readiness. No log bundle was published; independent review is pending.
