# Grok adapter

Status: experimental, adapter 0.2.0. Configuration reviewed on 2026-09-11; no named-harness versions or platforms certified for this revision. Refs [#30](https://github.com/october-dev/agent-intercom/issues/30).

A configuration fixture is not a successful run in the actual host.

Follow [shared setup](../README.md), then print a personalized snippet:

```sh
agentintercom harness config grok --scope my-project --agent grok-reviewer --name "Grok Reviewer"
agentintercom doctor --harness grok --scope my-project
```

Configuration location: `~/.grok/config.toml`. The [template](config.toml.example) contains placeholders; use the generator, not the template verbatim. The generator never edits existing configuration or stores credentials in it. Review and merge just the Agent Intercom entry. Retain the host's own tool approvals and workspace trust controls.

The snippet is TOML, matching the host's own `[mcp_servers.<name>]` sections; merge the named entry into the existing file by hand. `grok mcp` manages the same entries from the CLI.

Use `check_inbox` with `waitMs=1000` between work steps. The bridge registers and heartbeats outside the model loop, and attempts retirement when the host closes MCP. A hard kill relies on lease expiry. Each simultaneously connected window/project needs a distinct `--agent` ID; identical IDs deliberately replace the previous execution. Remove this server entry to disconnect, and verify retirement in the Bus before reassigning work.

Before promotion, run the full [compatibility runbook](../../compatibility/RUNBOOK.md), both directions with an independent harness, denied-tool cases, reconnection/replacement, and exact-candidate evidence with model, launch mode and public sanitized artifacts. [Upstream configuration documentation](https://github.com/xai-org/grok-build/blob/main/crates/codegen/xai-grok-pager/docs/user-guide/07-mcp-servers.md).
