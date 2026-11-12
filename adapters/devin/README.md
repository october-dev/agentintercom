# Devin

Experimental adapter 0.2.0. No current harness version is certified.

Run `agentintercom harness config devin --scope my-project --agent devin-reviewer` and review the result before adding the Agent Intercom entry to Devin Customize > MCPs > Add custom MCP (STDIO fields). Never paste the raw placeholder template. See [shared setup](../README.md) for scope creation, linking and removal.

Only a sandbox-colocated deployment is in scope. Generate this configuration inside that sandbox after starting Bus and creating its local scope. A laptop path or localhost address is not reachable from Devin. Account permissions and sandbox lifecycle are unverified.

Use `check_inbox` with `waitMs: 10000`. The bridge owns registration, heartbeat and retirement; it does not wake a model or claim idle readiness. Peer messages are untrusted input and never grant permissions. Keep host approval controls enabled.

Configuration follows [upstream documentation](https://docs.devin.ai/work-with-devin/mcp), reviewed 2026-09-10. Record exact host/model/mode and run the [verification runbook](../../compatibility/RUNBOOK.md) with an independent peer before changing status.
