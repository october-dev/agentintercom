# Codex adapter

Status: experimental, adapter 0.2.0. Configuration reviewed on 2026-09-10; no named-harness versions or platforms certified for this revision. Refs [#36](https://github.com/october-dev/agent-intercom/issues/36).

Historical adapter 0.1.0 has [Codex 0.152.1 / rc.4 evidence](../../compatibility/evidence/codex-0.152.1-macos-arm64.json). It does not certify this new adapter revision.

Follow [shared setup](../README.md), then print a personalized snippet:

```sh
agentintercom harness config codex --scope my-project --agent codex-reviewer --name "Codex Reviewer"
agentintercom doctor --harness codex --scope my-project
```

Configuration location: `.codex/config.toml`. The [template](config.toml.example) contains placeholders; use the generator, not the template verbatim. The generator never edits existing configuration or stores credentials in it. Review and merge just the Agent Intercom entry. Retain the host's own tool approvals and workspace trust controls.

Keep CLI, IDE, desktop, and remote evidence separate when the host provides multiple modes.

For a terminal session with a persistent native listener, the current source also
provides `agentintercom session start codex --workspace <name>` (or `--hosted <profile>`).
See [session connections](../../docs/connect-sessions.md#start-a-codex-session-with-native-wake)
for its private per-session connection, normal permission prompts, and experimental
app-server dependency. This is a separate launch mode from the pull-only MCP
configuration above; it does not change this adapter's certification status.

A host controller that already owns the execution (October Desktop over SSH) does not use this self-registering configuration. It points the same binary at a private connection file, `mcp stdio --connection-file <path>`, and installs `agentintercom hook <event> codex` as the lifecycle hook; see [managed connections](../../docs/managed-connections.md).

Use `check_inbox` with `waitMs=1000` between work steps. The bridge registers and heartbeats outside the model loop, and attempts retirement when the host closes MCP. A hard kill relies on lease expiry. Each simultaneously connected window/project needs a distinct `--agent` ID; identical IDs deliberately replace the previous execution. Remove this server entry to disconnect, and verify retirement in the Bus before reassigning work.

Before promotion, run the full [compatibility runbook](../../compatibility/RUNBOOK.md), both directions with an independent harness, denied-tool cases, reconnection/replacement, and exact-candidate evidence with model, launch mode and public sanitized artifacts. [Upstream configuration documentation](https://developers.openai.com/codex/mcp).
