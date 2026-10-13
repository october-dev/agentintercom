# Claude Code adapter

Status: experimental, adapter 0.2.0. Configuration reviewed on 2026-09-10; no named-harness versions or platforms certified for this revision. Refs [#37](https://github.com/october-dev/october-bus/issues/37).

The [earlier attempt](../../compatibility/observations/claude-code-2.1.251-macos-arm64.md) lacked an authenticated host; it is preserved as an observation. An authenticated [Claude Code 2.1.282 print-mode attempt](../../compatibility/observations/claude-code-2.1.282-macos-arm64.md) against runtime rc.5 completed the required scenario in both directions and is recorded as a partial, unreviewed observation. A [Claude Code 2.1.286 print-mode run on Linux x86_64](../../compatibility/observations/claude-code-2.1.286-linux-x86_64.md) of the terminal-session bridge repeated that scenario plus duplicate-session replacement and rotation; it is also partial and unreviewed.

Follow [shared setup](../README.md), then print a personalized snippet:

```sh
october-bus harness config claude-code --scope my-project --agent claude-code-reviewer --name "Claude Code Reviewer"
october-bus doctor --harness claude-code --scope my-project
```

Configuration location: `.mcp.json`. The [template](mcp.json.example) contains placeholders; use the generator, not the template verbatim. The generator never edits existing configuration or stores credentials in it. Review and merge just the October Bus entry. Retain the host's own tool approvals and workspace trust controls.

Keep CLI, IDE, desktop, and remote evidence separate when the host provides multiple modes.

## Walkthrough with the public daemon

This connects Claude Code to a local October Bus daemon, with no October Desktop. A second harness, Codex here, acts as the peer; any [adapter](../README.md) with a distinct agent ID works.

1. Start the daemon in its own terminal and leave it running:

   ```sh
   october-bus start
   ```

2. In another terminal, create a scope. The command prints the scope token and saves it in the private data directory. Keep that output in your own terminal: never paste it into a host, its configuration or a prompt.

   ```sh
   october-bus scope create my-project
   ```

3. From the Claude Code project directory, generate the server entry:

   ```sh
   october-bus harness config claude-code --scope my-project --agent claude-reviewer --name "Claude Reviewer" --output october-bus.mcp.json
   ```

   `--output` only creates a new file. If the project has no `.mcp.json`, rename this file to `.mcp.json`. Otherwise copy only its `october_bus` entry into the existing `mcpServers` object. The entry holds paths and IDs, never a token.

4. Generate the peer's entry with a different agent ID and add it to that host's configuration, for example `october-bus harness config codex --scope my-project --agent codex-builder`.

5. Launch Claude Code in the project. Approve the `october_bus` project server and each tool through Claude Code's own prompts. To pre-approve tools, list the ones you trust (`mcp__october_bus__<tool>`) under `permissions.allow` in the project settings. Claude Code 2.1.286 ignores those entries until the workspace is trusted. In print mode (`claude -p`) you can pass the same names with `--allowedTools`. Do not bypass permissions. Launch the peer the same way.

6. Once both agents have registered, link them and check the setup:

   ```sh
   october-bus link --scope my-project claude-reviewer codex-builder
   october-bus doctor --harness claude-code --scope my-project
   ```

7. Ask each agent to coordinate. Queued messages do not start a model turn, so prompts must ask for an inbox check:

   - "Call `list_peers`, then send `codex-builder` a notification with `message_peer`."
   - "Call `check_inbox` with `waitMs` 1000, acknowledge what you accepted, and answer the request with `message_peer` mode `response`. Then call `message_receipt` on the request."
   - "Add a task with `add_task`, claim it with `claim_task`, and finish it with `complete_task`."
   - "Ask the human for approval with `ask_user`."

8. Check results as the scope owner, in your own terminal. Export the token from step 2 there only:

   ```sh
   export OCTOBER_BUS_SCOPE_TOKEN=<scope-token>
   october-bus agent list
   october-bus task list
   october-bus status    # prints the daemon address
   curl -s -H "Authorization: Bearer $OCTOBER_BUS_SCOPE_TOKEN" <address>/v1/scope/escalations
   curl -s -X POST -H "Authorization: Bearer $OCTOBER_BUS_SCOPE_TOKEN" -H "Content-Type: application/json" \
     -d '{"answer":"approved"}' <address>/v1/scope/escalations/<escalation-id>/resolve
   ```

   Only the scope owner can resolve an escalation; the agent cannot answer its own.

9. To disconnect, close Claude Code or remove the `october_bus` entry. Run `october-bus agent list` again and confirm `claude-reviewer` is offline and unreachable before reassigning its work.

## Session lifecycle

A host controller that already owns the execution (October Desktop over SSH) does not use this self-registering configuration. It points the same binary at a private connection file, `mcp stdio --connection-file <path>`, and installs `october-bus hook <event>` as the lifecycle hook. For Claude Code, omit the flavor, as the [Desktop remote example](../../examples/desktop-remote/claude.settings.json) does. The default form is the Claude hook, and only it pulls staged context at session start; adding an explicit `claude` flavor skips that pull. See [managed connections](../../docs/managed-connections.md).

Use `check_inbox` with `waitMs=1000` between work steps. The bridge registers and heartbeats outside the model loop, and attempts retirement when the host closes MCP. A hard kill relies on lease expiry. Each simultaneously connected window/project needs a distinct `--agent` ID; identical IDs deliberately replace the previous execution.

The execution can end while Claude Code is still open. This happens when another window registers the same agent ID, the scope token is rotated, the lease expires (for example after a long sleep), or the daemon stops or becomes unreachable. The server then stays connected, but every tool call fails with "October Bus execution ended". It never registers again by itself, so an older window cannot take the ID back from a newer one. Resolve any duplicate agent ID first. Then reconnect `october_bus` from `/mcp`, or restart the window, to register a new execution.

Remove this server entry to disconnect, and verify retirement in the Bus before reassigning work.

Before promotion, run the full [compatibility runbook](../../compatibility/RUNBOOK.md), both directions with an independent harness, denied-tool cases, reconnection/replacement, and exact-candidate evidence with model, launch mode and public sanitized artifacts. [Upstream configuration documentation](https://code.claude.com/docs/en/mcp).
