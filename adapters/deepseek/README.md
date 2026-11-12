# DeepSeek adapter

Status: experimental, adapter 0.2.0. Configuration reviewed on 2026-09-11; no named-harness versions or platforms certified for this revision. Refs [#41](https://github.com/october-dev/agentintercom/issues/41).

A configuration fixture is not a successful run in the actual host.

Follow [shared setup](../README.md), then print a personalized snippet:

```sh
agentintercom harness config deepseek --scope my-project --agent dsh-reviewer --name "DeepSeek Reviewer"
agentintercom doctor --harness deepseek --scope my-project
```

Configuration location: `cordis.yml` in the [DeepSeek Harness](https://github.com/deepseek-ai/deepseek-harness) (`dsh`) profile configuration. The [template](cordis.yml.example) contains placeholders; use the generator, not the template verbatim. The generator never edits existing configuration or stores credentials in it. Review and merge just the Agent Intercom entry, which merges into the dsh plugin list as one `@deepseek-ai/dsh-mcp-client` server entry. Retain the host's own tool approvals and workspace trust controls.

The snippet uses JSON syntax, which is valid YAML; merge the named entry into the existing plugin list by hand.

Use `check_inbox` with `waitMs=1000` between work steps. The bridge registers and heartbeats outside the model loop, and attempts retirement when the host closes MCP. A hard kill relies on lease expiry. Each simultaneously connected window/project needs a distinct `--agent` ID; identical IDs deliberately replace the previous execution. Remove this server entry to disconnect, and verify retirement in the Bus before reassigning work.

The harness is a developer preview with announced compatibility-breaking changes; re-verify this configuration against a pinned released `dsh` version before relying on it.

Before promotion, run the full [compatibility runbook](../../compatibility/RUNBOOK.md), both directions with an independent harness, denied-tool cases, reconnection/replacement, and exact-candidate evidence with model, launch mode and public sanitized artifacts. [Upstream configuration documentation](https://github.com/deepseek-ai/deepseek-harness/blob/master/packages/mcp/mcp-client/README.md).
