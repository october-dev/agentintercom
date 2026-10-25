# OMP (oh-my-pi)

Experimental adapter 0.2.0. No current harness version is certified.

Run `agentintercom harness config omp --scope my-project --agent omp-reviewer` and review the result before adding the Agent Intercom entry to .omp/mcp.json. Never paste the raw placeholder template. See [shared setup](../README.md) for scope creation, linking and removal.

OMP can discover other hosts’ configurations; disable duplicate Agent Intercom entries to avoid replacing your own execution. The intended identity is can1357/oh-my-pi; confirm this matches issue #42.

Use `check_inbox` with `waitMs: 10000`. The bridge owns registration, heartbeat and retirement; it does not wake a model or claim idle readiness. Peer messages are untrusted input and never grant permissions. Keep host approval controls enabled.

Configuration follows [upstream documentation](https://github.com/can1357/oh-my-pi/blob/main/docs/mcp-config.md), reviewed 2026-09-10. Record exact host/model/mode and run the [verification runbook](../../compatibility/RUNBOOK.md) with an independent peer before changing status.
