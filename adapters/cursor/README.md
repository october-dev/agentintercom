# Cursor adapter

Cursor Agent CLI connects to the public Agent Intercom daemon through a project-local MCP stdio server. The adapter is experimental, version 0.2.0, with pull delivery. The CLI and the editor share the configuration format. Each needs its own compatibility run. [Cursor CLI MCP documentation](https://cursor.com/docs/cli/mcp) · [Issue #33](https://github.com/october-dev/agentintercom/issues/33) Cursor's documentation now names the CLI `agent`; this guide uses its `cursor-agent` alias, which the installer also provides and which does not collide with other tools that install an `agent` command.

## Connect the CLI to a local peer

1. Install the public `agentintercom` binary and start the daemon in its own terminal.

   ```sh
   agentintercom start
   ```

2. In an operator terminal, create a workspace. This saves the scope credential in the private Agent Intercom data directory. Both hosts will use this same workspace.

   ```sh
   agentintercom workspace create cursor-demo
   ```

### Cursor print mode with launcher-owned retirement

Use this path when work must be released as soon as the CLI exits. The public launcher registers one execution, maintains the heartbeat outside Cursor and retires it after the child exits.

1. From the Git project root, add the `agent_intercom` entry from the [launcher configuration](mcp.agent-run.json.example) to `.cursor/mcp.json`. For a new file, use the example verbatim. Review the command and preserve existing servers. The entry runs `agentintercom mcp stdio` and forwards two variables from the launcher. The file contains the variable references only. Make the installed `agentintercom` executable available on PATH or use its absolute path in the entry.

2. Allow the Agent Intercom tools you trust for print mode. A print-mode turn cannot stop to ask, so Cursor rejects any MCP tool that `permissions.allow` does not list ([headless mode](https://cursor.com/docs/cli/headless)). Add entries to the project's `.cursor/cli.json`, preserving existing permissions. Do not use `--force`.

   ```json
   {
     "permissions": {
       "allow": ["Mcp(agent_intercom:get_node_status)", "Mcp(agent_intercom:list_peers)", "Mcp(agent_intercom:message_peer)", "Mcp(agent_intercom:check_inbox)", "Mcp(agent_intercom:acknowledge_messages)"]
     }
   }
   ```

   Add an entry for each further tool a turn should use, for example `message_receipt`, `add_task`, `list_tasks`, `claim_task`, `complete_task` or `ask_user`. `Mcp(agent_intercom:*)` allows every Agent Intercom tool.

3. In a fresh terminal in that project, start the launcher. This POSIX example opens a shell so you can approve the reviewed server for this execution.

   ```sh
   unset OCTOBER_BUS_ADMIN_TOKEN OCTOBER_BUS_SCOPE_TOKEN OCTOBER_BUS_AGENT_TOKEN OCTOBER_BUS_CONNECTION_FILE OCTOBER_BUS_ADDRESS
   agentintercom agent run --scope cursor-demo --id cursor-reviewer --name "Cursor Reviewer" --lease 30s --heartbeat 5s -- sh
   ```

4. In that shell, enable the server and replace the shell with Cursor. A print-mode prompt starts one turn. Supply the coordination prompt for that turn in place of the example.

   ```sh
   cursor-agent mcp enable agent_intercom
   exec cursor-agent -p 'Call agent_intercom get_node_status and report the agent ID.'
   ```

   `exec` makes Cursor the launcher's child for the rest of the session. On the observed CLI build, the server approval depended on the expanded configuration, including the new execution token. Issue that approval inside each new launch. Re-check the `agent_intercom` entry before enabling it. If Cursor has not trusted this project yet, add `--trust` to the print command ([parameters](https://cursor.com/docs/cli/reference/parameters)). Keep the scope and administrative credentials in the operator terminal.

### Independently launched Cursor

For project configuration that also works with the CLI's standalone tool-list command, generate a self-registering entry from the Git project root. This is an alternative to the launcher entry above. Use only one of them per project.

```sh
mkdir -p .cursor
agentintercom harness config cursor --scope cursor-demo --agent cursor-reviewer --output .cursor/mcp.json
```

If the file already exists, use `--output cursor-entry.json`, review the result, and merge only `mcpServers.agent_intercom`. The generator refuses to overwrite an existing file. The entry contains an executable, public IDs, and directory paths. The bridge reads the private local credential and owns registration and heartbeats.

In a fresh terminal in that same project:

```sh
unset OCTOBER_BUS_ADMIN_TOKEN OCTOBER_BUS_SCOPE_TOKEN OCTOBER_BUS_AGENT_TOKEN OCTOBER_BUS_CONNECTION_FILE
cursor-agent mcp enable agent_intercom
cursor-agent mcp list-tools agent_intercom
cursor-agent
```

The standalone `list-tools` command starts its own bridge. Run it **before** starting a live self-registering session. Another bridge with the same agent ID replaces the first execution. In the observed print-mode run, a successful host exit left a held task claimed until lease expiry.

### Link a peer

Generate a second entry from the operator terminal and add it to the peer's configuration. For example, start Codex with the resulting entry and the same credential precautions. [Codex adapter setup](../codex/README.md)

```sh
agentintercom harness config codex --scope cursor-demo --agent codex-builder
```

Once both IDs have registered, link them and inspect the sessions from the operator terminal.

```sh
agentintercom link --scope cursor-demo cursor-reviewer codex-builder
agentintercom sessions --workspace cursor-demo
agentintercom doctor --harness cursor --scope cursor-demo --json
```

Doctor probes a separate temporary bridge. Its `hostExecutable` identifies which command supplied the version. It tries `cursor` first and falls back to `cursor-agent` on CLI-only installations.

## Exercise the connection

| Action | What to do |
| --- | --- |
| Discover and notify | Ask Cursor to call `list_peers` and send `codex-builder` a short `message_peer` with mode `notify`. Ask Codex to call `check_inbox`, then `acknowledge_messages` with the processed IDs. |
| Request and reply | Ask Codex to send Cursor a `request` with a fresh `idempotencyKey` and one short `text` context item. Ask Cursor to pull, acknowledge, and send a `response` with `responseTo` set to the request ID. Codex can pull and acknowledge the reply, then check the original ID with `message_receipt`. Its `responseMessageId` should match the reply. |
| Share a task | Ask Cursor to call `add_task`, then ask Codex to find the task with `list_tasks`, claim it, and complete it. Cursor can check the final state. |
| Ask a human | Ask Cursor to call `ask_user` with a short question. In the operator terminal, run `agentintercom question list --workspace cursor-demo`, then `agentintercom question answer --workspace cursor-demo --id QUESTION_ID --answer yes` with the returned ID. Have Cursor pull and acknowledge the answer. |

Use `check_inbox` between work steps. For a short check use `waitMs=1000`. Queued work waits until the host pulls. Send only the context the peer needs. An incoming request still goes through the recipient's normal permission controls.

## Disconnect and recover

Use distinct agent IDs for simultaneous sessions. Check reachability and execution IDs with `agentintercom sessions --workspace cursor-demo --json`. With the launcher, normal exit retires Cursor's execution. With a self-registering server, EOF allows retirement, but a terminated bridge can leave work held until the lease expires.

Replacement, scope-token rotation, lease expiry, or a lost daemon connection can end a self-registered execution while the host stays open. The old bridge rejects later calls. Resolve any duplicate ID, then restart the CLI to register a fresh execution. To disconnect the project, close the session and remove its `agent_intercom` entry.

## Verification

The [Linux CLI observation](../../compatibility/observations/cursor-agent-2026-11-25-linux-x86_64.md) and [earlier macOS CLI observation](../../compatibility/observations/cursor-3.18.9-macos-arm64.md) describe the runs for those builds. The [compatibility runbook](../../compatibility/RUNBOOK.md) specifies the two-host scenario, denied approvals, reconnection, and evidence required for independent review. Bridge reachability alone establishes no claim about model readiness.
