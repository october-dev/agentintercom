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

## Shared tasks

The same two shells can split work through shared tasks. In the first shell:

```sh
agentintercom task add --title 'Build' --json
# Use the returned task ID below.
agentintercom task add --title 'Ship' --depends-on TASK_ID
```

In the second shell:

```sh
agentintercom task list --ready
agentintercom task claim --id TASK_ID
agentintercom task progress add --id TASK_ID --text 'Tests pass; packaging next'
echo 'Built and tested on the release branch' | agentintercom task complete --id TASK_ID --note-file -
```

Either shell can read a task's history with `agentintercom task progress list --id TASK_ID`.
`--kind blocker` or `--kind note` labels a progress entry; the default is
`progress`. Use `--text-file` or `--note-file` (with `-` for stdin) to keep
private text out of shell history, and `--json` for scripts. Progress text is
limited to 4000 bytes and a completion note to 16 KiB.

`claim`, `release`, `complete` and `progress add` always act as the connected
session, using `OCTOBER_BUS_AGENT_TOKEN`. Only the claiming execution can release
or complete a task. Another session's claim, an unfinished dependency, or a
replaced session returns an error instead of taking the task over. `task add` and
`task list` use `OCTOBER_BUS_SCOPE_TOKEN` when it is set; inside `agent run`, which
removes that token, they use the session's credential.
