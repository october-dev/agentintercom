# Kilo Code adapter

Status: experimental, adapter 0.2.0. Configuration reviewed on 2026-09-10; no named-harness versions or platforms certified for this revision. Refs [#58](https://github.com/october-dev/agent-intercom/issues/58).

A configuration fixture is not a successful run in the actual host.

Follow [shared setup](../README.md), then print a personalized snippet:

```sh
agentintercom harness config kilo-code --scope my-project --agent kilo-code-reviewer --name "Kilo Code Reviewer"
agentintercom doctor --harness kilo-code --scope my-project
```

Configuration location: `.kilo/kilo.json`. The [template](mcp.json.example) contains placeholders; use the generator, not the template verbatim. The generator never edits existing configuration or stores credentials in it. Review and merge just the Agent Intercom entry. Retain the host's own tool approvals and workspace trust controls.

This targets the current documented kilo.json MCP format, not the older Cline-style configuration. Do not inherit Cline verification.

Use `check_inbox` with `waitMs=1000` between work steps. The bridge registers and heartbeats outside the model loop, and attempts retirement when the host closes MCP. A hard kill relies on lease expiry. Each simultaneously connected window/project needs a distinct `--agent` ID; identical IDs deliberately replace the previous execution. Remove this server entry to disconnect, and verify retirement in the Bus before reassigning work.

Before promotion, run the full [compatibility runbook](../../compatibility/RUNBOOK.md), both directions with an independent harness, denied-tool cases, reconnection/replacement, and exact-candidate evidence with model, launch mode and public sanitized artifacts. [Upstream configuration documentation](https://kilo.ai/docs/automate/mcp/using-in-kilo-code).
