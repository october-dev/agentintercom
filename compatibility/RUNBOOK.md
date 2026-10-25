# Harness verification runbook

Use this runbook to exercise a released harness and adapter through public Agent Intercom interfaces. Run it on a clean machine for every platform listed in the evidence record.

## Setup

1. Install the Agent Intercom release being tested.
2. Start the daemon and create a new scope.
3. Generate each host's config with `harness config <host> --scope <scope> --agent agent-a` (and `agent-b`). Use separate agent IDs and preserve host approvals.
4. Launch one independent controller host and the harness under test normally, without `agent run` or inherited Bus tokens; link them with `link --scope <scope> agent-a agent-b`. Test managed mode separately if advertised. For Pi use its native extension instructions.
5. Confirm no Bus credentials appear in host configuration, arguments, model context or logs. The token-file bridge owns scope/execution credentials; same-user filesystem access is not sandbox isolation.

Keep registration, heartbeat, execution replacement, and cleanup in the adapter. Do not ask the model to maintain its own lease.

## Required scenario

1. Both agents discover each other by exact agent ID.
2. Each agent sends and acknowledges one durable notification.
3. `agent-a` sends a request with a fresh idempotency key and bounded context.
4. Retrying that logical request returns the original message ID.
5. `agent-b` acknowledges the request and sends one linked response.
6. `agent-a` acknowledges the response and calls `message_receipt` with the original `messageId`, verifying `responseMessageId`. An unrelated agent's receipt lookup must fail.
7. `agent-a` creates a task and a second task that depends on it.
8. Claiming the blocked task fails.
9. `agent-b` claims, releases, reclaims, and completes the first task.
10. `agent-a` claims and completes the newly ready task.
11. `agent-b` creates a human escalation. Only the scope owner resolves it.
12. Restart `agent-b` and verify the previous execution loses authority.
13. Stop both harnesses and verify retirement: offline/unreachable, old token rejected, reservations and claims released. Repeat one run with an unclean exit and verify lease recovery.

Also cancel a waiting tool call and immediately make another call; cancellation must not poison the bridge. Test duplicate-window replacement, host restart/reload, a rejected tool approval and a missing/rotated local scope token. Preserve genuine JSON arrays in host inputs; record any bridge coercion of stringified arrays. Direct HTTP remains strict. Start inbox waits at 10 seconds and separately record host startup, discovery and tool-call timeout settings.

Record every host prompt, approval, limitation, result and version, plus `launchMode` (for example `config-only`, `managed` or `native-extension`) and `model` (provider and exact model ID). A model retry that corrects its own invalid tool input is allowed but must be recorded. A local doctor's 15-tool check is not a substitute for these real-host scenarios.

## Evidence

Use the [maintainer-assisted verification workflow](VERIFICATION.md) when an account or platform is unavailable, or to package sanitized logs for review. Partial and not-run attempts are observations, not formal passing evidence.

Create one JSON record that validates against [`compatibility-evidence.schema.json`](../spec/0.1/schemas/compatibility-evidence.schema.json). Hash the complete run log with SHA-256 and place that digest in `resultDigest`.

Review and sanitize the log before publication, preserving the scenario evidence. `resultDigest` must hash the exact published sanitized bytes; include an HTTPS `attestation` link to those artifacts. Redaction tooling does not replace manual privacy or compatibility review.

A passing record must use a public repository commit and a released harness version. Add it to `registry.json` only after independent review confirms the required profile passed.
