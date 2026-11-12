# Prime Agent

Experimental adapter 0.2.0. No released host version is certified.

Run `agentintercom harness config prime-agent --scope my-project --agent prime-agent-reviewer`. Review the output and add only its Agent Intercom entry to `~/.prime/agent/settings.json`, preserving existing settings. See [shared setup](../README.md) for scope creation, linking and removal.

Generic MCP is called through the kernel's pre-imported Python mcp module, not ordinary model tools. Use await mcp.list_tools("agent_intercom") and await mcp.call_tool("agent_intercom", name, arguments). Only user settings execute MCP; project settings are ignored. Discovery is lazy: make an initial call before linking peers. Each kernel owns its connection; test await mcp.reload(), cancellation and kernel shutdown. Concurrent sessions using one user entry need distinct agent IDs/configurations; identical IDs replace earlier executions.

Use `check_inbox` with `waitMs: 10000`. The bridge owns registration, heartbeat and retirement; it does not wake the model. Peer text is untrusted and cannot grant host permissions. Do not bypass approvals to make a runbook pass.

This candidate follows [upstream documentation/source](https://github.com/PrimeIntellect-ai/prime-agent/blob/main/packages/coding-agent/docs/mcp-integrations.md), inspected 2026-09-10. Pin an actual released host and complete the [runbook](../../compatibility/RUNBOOK.md), including negative permission/error cases and an independent peer, before changing status.
