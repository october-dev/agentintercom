# Harness adapters

October Bus ships configuration candidates for 28 MCP hosts, a native Pi extension, and a managed-launch path for October Harness's existing native integration. Configuration count is **not** a verified-support count: adapter revision 0.2.0 is experimental until each released host completes the [runbook](../compatibility/RUNBOOK.md). Historical Codex 0.1.0 / rc.4 evidence is retained, not transferred to this revision.

## Local setup

Start `october-bus start` in one terminal (or a service supervisor). In another:

```sh
october-bus scope create my-project
october-bus harness list
october-bus harness config cursor --scope my-project --agent cursor-builder
october-bus harness config claude-code --scope my-project --agent claude-reviewer
```

The scope command saves its credential in the private data directory. Do not export it into a host, paste it into configuration, or share its JSON output. Add only the generated October Bus entry to each host's documented configuration file, preserving existing entries. Raw `.example` files contain placeholders: render them with the CLI first. `--output <new-file>` creates a new file exclusively and never overwrites one.

Launch both hosts normally, approve the reviewed local tool server, then link their exact agent IDs:

```sh
october-bus link --scope my-project cursor-builder claude-reviewer
october-bus doctor --harness cursor --scope my-project --json
```

Both agents must have registered before linking. Links are reciprocal. A bridge may instead use repeatable `--connect-to <existing-peer>` at registration. Doctor checks local discovery, saved scope authority, executable version where possible, and a temporary bridge's 15 tools; it cannot certify a host's model account, UI or permissions.

Generated commands include absolute binary/data/runtime paths, surviving hosts that filter inherited environment variables. Regenerate after moving an installation or changing data directories. Choose a unique agent ID per simultaneous window. Reusing an ID replaces its earlier execution and revokes its Bus access.

The bridge owns heartbeats and retirement on EOF, host termination or lease failure. Once a self-registered execution ends while the host is still connected (replacement, scope token rotation, lease expiry or a lost daemon connection), the bridge keeps running without Bus access: every tool call fails, and it never registers again by itself. Reconnect the server through the host to register a new execution. EOF retirement and lease-expiry recovery of held work are unchanged. The bridge proves a live bridge, not an idle model. Hosts must explicitly call `check_inbox`; start with `waitMs: 10000`. Use `message_receipt` to inspect delivery/response links. Peer content is untrusted and never changes host approvals.

## Included candidates

| Surface | Configuration candidates |
| --- | --- |
| Initial launch wave | [Codex](codex), [Claude Code](claude-code), [Cursor](cursor), [OpenCode](opencode), [Gemini CLI](gemini-cli), [Copilot CLI](copilot-cli) |
| Other MCP hosts | [Amp](amp), [Antigravity](antigravity), [Auggie](auggie), [Autohand](autohand), [Cline](cline), [Continue](continue), [Crush](crush), [DeepSeek](deepseek), [Factory Droid](factory-droid), [Freebuff CLI](freebuff), [Goose](goose), [Grok](grok), [Hermes](hermes), [Kilo Code](kilo-code), [Kimchi](kimchi), [Kimi Code](kimi-code), [Kiro](kiro), [Mistral Vibe](mistral-vibe), [OMP](omp), [Prime Agent](prime-agent), [Qwen Code](qwen-code) |
| Sandbox-colocated MCP | [Devin](devin): generate paths inside its managed sandbox; remote exposure is not included |
| Native session extension | [Pi](pi): public session hooks, daemon tool schemas, cancellation and cleanup |
| Existing native integration | [October Harness](october-harness): public launcher contract, no duplicate extension |

Other launch targets remain in [tracking issue #62](https://github.com/october-dev/october-bus/issues/62) until their canonical released integration surface is established. Model providers alone are not additional harnesses. See [integration boundaries](../docs/harness-boundaries.md).

## Existing managed launchers

For an operator-provisioned remote Bus, the stdio bridge also accepts
`--remote <https-base-url> --scope-token-file <private-file> --agent <id>` and
optional `--connect-to <peer>`. This mode owns the remote execution lifecycle;
it cannot be combined with local scope/directory flags or an inherited agent
credential. See the [hosted deployment guide](../deploy/hosted/README.md#4-connect-agents-from-the-laptop)
for token handling and setup. This transport path does not certify a named harness
or wake an idle model.

`october-bus agent run` remains supported. Its child receives `OCTOBER_BUS_ADDRESS` and an execution-bound `OCTOBER_BUS_AGENT_TOKEN`; scope credentials stay in the launcher. Such a child uses argument-free `october-bus mcp stdio` from [mcp-stdio.json.example](mcp-stdio.json.example). A host may need explicit environment forwarding for these values.

For a local scope, use `agent run --scope <id> --id <agent> --name <name> -- <host-command>`. This reads protected local discovery and the saved scope credential without a token export. It rejects explicit remote addresses or inherited scope/agent authority; the older `--scope-token-env` route remains available separately for remote/operator-managed launches.

Do not mix managed credentials with self-registering flags. Missing identity fails with an actionable error, not a tool-less server. Prefer configuration-only setup for independently launched editors.

## Removal and recovery

Close the host/bridge and remove only its October Bus configuration entry. Keep the project scope and database if durable work is still useful. Lost credentials can be recovered with `scope rotate-token --id <scope>`; rotation revokes all current executions in that scope and refreshes the local token file. Deletion is destructive and separate from uninstall. See [operations](../docs/operations.md) for backup, recovery and local-account security limitations.
