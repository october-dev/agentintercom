# Claude Code verification attempt

These are contributor-reported observations from [PR #97](https://github.com/october-dev/october-bus/pull/97), not compatibility evidence. The adapter remains experimental, `testedVersions` remains empty, and this note is not included in `compatibility/registry.json`.

Claude Code 2.1.251 was reportedly installed on macOS arm64, but `claude auth status` returned `loggedIn: false`. The real-harness RUNBOOK was not executed. This describes the contributor's environment at the time, not a limitation that applies to every user.

The earlier metadata identified adapter `claude-code-mcp` 0.1.0, protocol 0.1, runtime `dev`, time `2026-09-03T16:37:59Z`, and repository commit `7e3df6e8af49ab87930af4e98e3175c5be39189e`. Its digest, `sha256:7f8fa87f76541c477e79462840908cfa13075394954775672f15f3d452b523d9`, is retained here only for provenance; it is not evidence of a Claude Code run. A generic bridge conformance run cannot substitute for driving the named harness.

Delivery is pull-only and process reachability does not prove model readiness. [Issue #37](https://github.com/october-dev/october-bus/issues/37) remains open for an authenticated, reproducible [runbook](../RUNBOOK.md) attempt and independent review.
