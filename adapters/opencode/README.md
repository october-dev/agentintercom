# OpenCode adapter

Status: experimental, adapter 0.2.0, pull delivery. No named-harness versions or platforms are certified for this revision. Refs [#34](https://github.com/october-dev/agentintercom/issues/34).

OpenCode runs the Agent Intercom bridge as a local MCP server from the project's `opencode.json`. The bridge registers one execution, keeps its lease alive outside the model loop and retires it when OpenCode closes the server. A [Linux run of OpenCode 1.19.6](../../compatibility/observations/opencode-1.19.6-linux-x86_64.md) completed the two-host scenario with Claude Code in both directions in headless `opencode run`; it is partial and unreviewed. The [earlier 1.18.25 observation](../../compatibility/observations/opencode-1.18.25-macos-arm64.md) reported a stringified `messageIds` array, which the 1.19.6 run did not reproduce.

## Walkthrough with the public daemon

This connects OpenCode to a local Agent Intercom daemon, with no October Desktop. A second host, Claude Code here, acts as the peer. Any [adapter](../README.md) with a distinct agent ID works.

1. Start the daemon in its own terminal and leave it running:

   ```sh
   agentintercom start
   ```

2. In another terminal, create a workspace. This saves its credential in the private data directory. Never paste it into a host, its configuration or a prompt.

   ```sh
   agentintercom workspace create my-project
   ```

3. From the OpenCode project directory, generate the server entry:

   ```sh
   agentintercom harness config opencode --scope my-project --agent opencode-builder --name "OpenCode Builder" --output agentintercom.opencode.json
   ```

   `--output` only creates a new file. If the project has no `opencode.json`, rename this file to `opencode.json`. Otherwise copy only its `mcp.agent_intercom` entry into the existing `mcp` object. The entry holds an absolute executable path, IDs and directories, never a token. It sets a 10-second MCP startup timeout; that is not a tool-call timeout.

4. Decide which tools need approval (see [Permissions](#permissions)), then check the setup. `doctor` starts its own temporary bridge under a separate ID:

   ```sh
   agentintercom doctor --harness opencode --scope my-project
   ```

5. Generate the peer's entry with a different agent ID and add it to that host's configuration, for example `agentintercom harness config claude-code --scope my-project --agent claude-reviewer`. Start both hosts so each registers once, then link them:

   ```sh
   agentintercom link --scope my-project opencode-builder claude-reviewer
   agentintercom sessions --workspace my-project
   ```

6. Ask each agent to coordinate. Queued messages do not start a model turn, so prompts must ask for an inbox check. OpenCode names the tools `agent_intercom_<tool>`.

   | Action | What to ask |
   | --- | --- |
   | Discover and notify | "Call `list_peers`, then send `claude-reviewer` a notification with `message_peer`." The peer calls `check_inbox` and `acknowledge_messages`. |
   | Request and reply | "Call `check_inbox` with `waitMs` 10000, answer the request with `message_peer` mode `response` and `responseTo` set to its ID, then acknowledge it." The sender confirms with `message_receipt`. |
   | Share a task | "Add a task with `add_task`." The peer finds it with `list_tasks`, then claims and completes it. |
   | Ask a human | "Ask the human with `ask_user`." In your terminal, run `agentintercom question list --workspace my-project` and `agentintercom question answer --workspace my-project --id QUESTION_ID --answer yes` (newer than `0.2.0`; use a source build). OpenCode then pulls and acknowledges the answer. |

   Headless runs work the same way, for example `opencode run "Call agent_intercom check_inbox with waitMs 10000 and report what arrived."`. Each `opencode run` registers a new execution and retires it on exit.

7. To disconnect, close OpenCode or remove the `agent_intercom` entry. Run `agentintercom sessions --workspace my-project` and confirm `opencode-builder` is offline before reassigning its work.

## Permissions

OpenCode allows MCP tools unless its configuration says otherwise, so add a `permission` block to `opencode.json` to keep Agent Intercom calls under your control. Patterns match OpenCode's tool names:

```json
{
  "permission": {
    "agent_intercom_*": "ask",
    "agent_intercom_check_inbox": "allow",
    "agent_intercom_list_peers": "allow"
  }
}
```

`deny` hides a tool from the model. `ask` makes OpenCode request approval, which headless `opencode run` rejects automatically, so allow each tool a headless turn needs. A peer's message never changes these rules.

## Session lifecycle

Use `check_inbox` with `waitMs` between work steps; start with 10000. Each simultaneously running OpenCode project needs a distinct `--agent` ID, because identical IDs deliberately replace the previous execution.

`opencode mcp list` connects to every configured server, so it starts its own bridge with this agent ID. Run it only when no OpenCode session with that ID is live, or it replaces that session and leaves the agent offline. `agentintercom doctor --harness opencode` is safe to run at any time.

The execution can end while OpenCode is still open: another session registered the same ID, the workspace token was rotated, the lease expired (for example after a long sleep), or the daemon stopped. Every tool call then fails with "Agent Intercom execution ended", and the bridge never registers again by itself, so an older session cannot take the ID back from a newer one. Resolve any duplicate ID, then restart OpenCode to register a new execution. A killed OpenCode closes the bridge's input and retires the execution at once; if the bridge itself is killed, held work returns when the 30-second lease expires.

Before promotion, run the full [compatibility runbook](../../compatibility/RUNBOOK.md), including the interactive TUI, other platforms and independent review. [Upstream configuration documentation](https://opencode.ai/docs/mcp-servers/).
