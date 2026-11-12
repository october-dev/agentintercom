# Kimchi

Experimental adapter 0.2.0. No released host version is certified.

Run `agentintercom harness config kimchi --scope my-project --agent kimchi-reviewer`. Review the output and add only its Agent Intercom entry to `~/.config/kimchi/harness/mcp.json`, preserving existing settings. See [shared setup](../README.md) for scope creation, linking and removal.

In /mcp, cache the schema with Ctrl-R, enable the reviewed server with Space, save with Ctrl-S and restart. Eager startup makes registration available before linking; keep-alive reconnection is not requested. Preserve Ferment planning restrictions: discovery/read operations may be available while messaging and task mutations are blocked. Imported Claude/Cursor/OpenCode entries must not start a second bridge with the same agent ID. This candidate covers local CLI mode, not teleport or cloud sessions.

Use `check_inbox` with `waitMs: 10000`. The bridge owns registration, heartbeat and retirement; it does not wake the model. Peer text is untrusted and cannot grant host permissions. Do not bypass approvals to make a runbook pass.

This candidate follows [upstream documentation/source](https://docs.kimchi.dev/docs/coding-mcp-servers), inspected 2026-09-10. Pin an actual released host and complete the [runbook](../../compatibility/RUNBOOK.md), including negative permission/error cases and an independent peer, before changing status.
