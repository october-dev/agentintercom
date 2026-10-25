# Cursor verification attempt

These are contributor-reported observations from [PR #96](https://github.com/october-dev/agent-intercom/pull/96), not reviewed compatibility evidence. The adapter remains experimental and this note is not included in `compatibility/registry.json`.

The contributor reports completing RUNBOOK steps 1–13 using headless `cursor-agent -p`, Cursor 3.18.9, adapter `cursor-mcp` 0.1.0, protocol 0.1, and Agent Intercom `v0.1.0-rc.4` on macOS arm64. The reported attempt time is `2026-09-06T00:37:00Z`, and the reported repository commit is `47acb0fd94cc476c5adffe383af03d03620ebd52`.

The supplied log digest is `sha256:57527f88d6dc49864f98a19809672ca533af1469dd7d5ff665d5c31c08f2e310`. No public run-log link accompanies this note, so the digest and full-profile outcome have not been independently checked. The generic bridge conformance command in the adapter manifest does not establish named-harness compatibility by itself.

Delivery is pull-only. Process reachability does not prove model readiness. The attempt did not exercise interactive approval prompts; other versions and platforms remain unverified.

[Issue #33](https://github.com/october-dev/agent-intercom/issues/33) remains open. To qualify for verified status, provide the complete reproducible run log, validate a formal record against the compatibility-evidence schema, and obtain independent review as described in the [runbook](../RUNBOOK.md).
