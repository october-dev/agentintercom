# Messaging from scripts and terminals

A connected process can use CLI commands with the same execution credential as
MCP. `agent run` supplies that credential and keeps its lease alive. It removes
workspace and administrator tokens from the child environment.

For example, start two shell sessions in a local workspace:

```sh
agentintercom agent run --scope project --id first --name 'First session' -- bash
agentintercom agent run --scope project --id second --name 'Second session' --connect-to first -- bash
```

In the first shell:

```sh
agentintercom message send --to second --mode request --body 'Review the change' --key review-1
```

In the second shell:

```sh
agentintercom inbox --wait 25s
# Use the request's actual message ID below.
agentintercom message send --to first --reply-to MESSAGE_ID --body 'Reviewed' --key review-reply-1
agentintercom message ack MESSAGE_ID
```

The first shell can inspect the request's state with
`agentintercom message receipt MESSAGE_ID`, or receive its response with `inbox`.
Message IDs address exact requests; session IDs address exact peers. An owner
must explicitly link peers. Labels are not mandatory roles.

Use `--body-file path` or `--body-file -` for file/stdin content, and `--json` for
scripts. Keep private message content out of shell history by using stdin.
`--expires 1h` is optional; its maximum is 30 days. The body limit is 64 KiB.

Acceptance means a message is durable, not that a model has processed it. Reading
an inbox commits delivery but does not acknowledge processing. A delivered,
unacknowledged message can be received again. Acknowledge only after completing
its work. Reuse an explicit `--key` when retrying the same send; changing content
under that key returns a conflict. Without a key, a repeated send is new work.

These commands use `OCTOBER_BUS_AGENT_TOKEN` and the launcher's Bus address. They
never use a workspace token as a substitute. Remote sends require HTTPS and
refuse redirects. A CLI connection does not itself wake an idle agent.

## Watching events and hook-driven inbox drains

`agentintercom watch --scope project` streams scope events as NDJSON over the
existing revision-cursor long-poll: the daemon wakes waiters when an event
lands, so latency is effectively push. `--agent <id>` keeps only events naming
one agent, `--type message.` filters by event type prefix, and `--once --from 0`
prints a single batch for scripts. `--wait` is the long-poll wait in
milliseconds and must be at least 1 unless `--once` is set.

Inside a managed execution, `agentintercom inbox inject` drains the pending
inbox as prompt-ready text for harness hooks, using the same execution
credential as `inbox` (the launcher's agent environment or
`--connection-file`). It never registers a new execution and refuses outside a
managed one. `--ack` acknowledges the printed messages; without it they return
on the next pull, so a per-turn hook would re-inject them. `--json` prints the
raw messages instead of prompt text. Injected peer messages are untrusted input
and never grant permissions.
