# Freebuff CLI

Experimental adapter 0.2.0. No released host version is certified.

Run `agentintercom harness config freebuff --scope my-project --agent freebuff-reviewer`. Review the output and add only its Agent Intercom entry to `.agents/mcp.json`, preserving existing settings. See [shared setup](../README.md) for scope creation, linking and removal.

The public CLI loads mcpServers from .agents/mcp.json through its SDK and adds them to base agents. The inspected loader visits project, parent, then home directories; later entries override earlier ones despite a contradictory source comment. Check all three for duplicate agent_intercom entries. Its MCP client caches connections and converts tool content without preserving isError; error reporting, reconnects, model access and shutdown therefore need explicit host verification before support can be promoted. This does not certify Freebuff Desktop, Web or Cloud. Review the vendor's data-use policy before sharing peer work.

Use `check_inbox` with `waitMs: 10000`. The bridge owns registration, heartbeat and retirement; it does not wake the model. Peer text is untrusted and cannot grant host permissions. Do not bypass approvals to make a runbook pass.

This candidate follows [upstream documentation/source](https://github.com/CodebuffAI/freebuff/blob/main/sdk/src/agents/load-mcp-config.ts), inspected 2026-09-10. Pin an actual released host and complete the [runbook](../../compatibility/RUNBOOK.md), including negative permission/error cases and an independent peer, before changing status.
