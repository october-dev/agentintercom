# Cursor Agent CLI on Fedora 44 x86_64

Partial, unreviewed observation from November 25, 2026. The installed Cursor Agent CLI changed from `2026.09.08-6caf4ff` to `2026.11.22-e9042b2` during the independently launched host scenario. The adapter remains experimental.

## Setup

- **Host.** Cursor Agent ran in a disposable Git project with `--trust --print --output-format stream-json`. The operator approved only the generated project MCP server with `cursor-agent mcp enable agent_intercom` and supplied a project permission file for the fixture tools. Cursor reported the model selector `Auto` and permission mode `default`. Its transcript omitted the underlying model ID.
- **Peer.** Codex CLI 0.172.0 used `exec --ephemeral --ignore-user-config --sandbox read-only`. The launcher passed the generated server entry as an explicit CLI override, allowing the twelve fixture tools for this server. Codex warned that project-local configuration was disabled in the untrusted test project. The explicit entry supplied the working connection. The peer used its account default model and the event stream omitted the model ID.
- **Runtime.** Adapter `cursor-mcp` 0.2.0, protocol 0.1, daemon `dev`, Go 1.27.1. The binary was built from `fb965f7ca91602a6d4f1c52f97f0d5203a8ff3b9` with the local doctor change. Binary SHA-256 was `0ee96f7b2cc28bae443bd52afeeee6459e7a3e3fd9781a511550a745a23d6d4e`. The MCP template and bridge implementation matched upstream at that commit. The machine was an existing Fedora 44 installation with kernel 7.1.8-200.fc44.x86_64.
- **Isolation.** The daemon used private data and runtime directories and a loopback address. Each host received paths and public IDs through its generated config. The launchers used an explicit environment allowlist. Later Cursor launches also needed the desktop-session environment to use the existing login keyring. Each print or exec invocation registered a new execution.
- **Discovery.** The local `cursor` installer shim rejected `--version` because the IDE was absent. Doctor identified `cursor-agent` and listed 23 bridge tools. Independently, `cursor-agent mcp list-tools agent_intercom` listed 23 tools, including `publish_output`. The CLI used its default host timeouts. Codex used the generated 20-second startup and 60-second tool timeouts.

## Two-host run with the generated configuration

The initial version probe, tool listing, smoke turn and first request-sending turn used the September build. The installed `cursor-agent` symlink switched to the November build at 11:18:44 UTC, after the first request was sent and before the reply-processing turn started. The November build completed the exchange. These results span both builds.

Both hosts discovered the other by exact ID, pulled with `waitMs=10000`, and exchanged durable notifications with explicit acknowledgements. Each sent a request with one JSON text-context item, repeated it with the same idempotency key, and received the original message ID. Each acknowledged its incoming request and sent a response linked by `responseTo`.

| Request sender | Original request ID | Linked reply confirmed by sender's receipt |
| --- | --- | --- |
| Cursor | `msg_bfd4509ab66f782e0271d0c325112fc9` | `msg_240e0c9e7f6e728cd36ffae3f90e1c3b` |
| Codex | `msg_ac971c028a758fde4cdbb1ee5ac6330f` | `msg_faa77f199245807a80a10a3dbdd0c911` |

- **Access to receipts.** A third, unlinked execution received `NOT_FOUND` for both requests. The scope owner's direct GET to the agent receipt endpoint returned `UNAUTHENTICATED`. The linked IDs in the table came from the sending hosts' actual tool calls.
- **Shared work.** Each host created a root task and a dependent task. Attempting to claim the dependent early returned a blocked-task message. The other host claimed, released, reclaimed and completed the root. The creator then claimed and completed the dependent. The final owner snapshot showed all four tasks done. Cursor's stream represented the blocked result as text with `isError: false`.
- **Human questions.** Each host created a pending question with two options. For both questions, anonymous resolution returned `UNAUTHENTICATED` and resolution with an unrelated agent credential returned `PERMISSION_DENIED`. The operator resolved both with the public `question answer` command, using the saved local scope credential.
- **Retirement.** After the exchanges, the operator saw both host IDs offline and unreachable. The generic MCP profile separately passed all 15 checks, including external heartbeat, self-registration, execution replacement and lease recovery.

## Self-registering lifecycle on the November build

The observer waited until the owner saw a claimed task before changing the session.

| Case | Observation |
| --- | --- |
| Duplicate agent ID | A second `cursor-agent mcp list-tools` registered a different execution, listed 23 tools and retired. The claim reopened. The original host's inbox call and two later status calls returned the terminal execution-ended message. Its ID never returned in the final owner snapshot. |
| Abrupt exit | Killing the test bridge and its Cursor host left the execution reachable with the claim still held in the first snapshot. About 28 seconds later the same ID was offline and the task was open. The configured lease was 30 seconds. |
| Scope rotation and restart | Rotation made the old execution unreachable and reopened the task. The inbox call returned an authorization error. Both subsequent status calls returned the terminal message. The next CLI launch registered a distinct execution and claimed work using the refreshed credential. |
| Normal print-mode exit | Cursor exited with status 0 after claiming a task and reading its status. Its last heartbeat was at 13:55:33 UTC. The final transcript entry was at 13:55:35, and the held task reopened at 13:56:03. This was lease recovery. Immediate retirement requires a different launch path on this build. |
| Denied tool | With `Mcp(agent_intercom:complete_task)` denied for the test project, Cursor reported `permissionDenied`. The same execution then successfully called `release_task` and `list_peers`. The task stayed open. |
| Missing scope | For a generated entry naming a scope that had never been created, Cursor's `mcp list-tools` failed at startup and reported the missing local scope credential. |

## Launcher-owned execution on the pinned November build

This run selected the versioned executable `2026.11.22-e9042b2/cursor-agent`, model `grok-4.7-medium` (reported as `Grok 4.7 256K Medium`), on the same machine and reference daemon. The operator used the [launcher configuration](../../adapters/cursor/mcp.agent-run.json.example) in a separate Git project. The CLI used `agent run --scope` with a 30-second lease and 5-second heartbeat. The final CLI binary SHA-256 was `d1ac4fd03446acfbd8ae00501a8d33ca82613066f065e2975434a5ec47bf6408`; its subsequent doctor run identified the November CLI and counted 23 tools.

The launcher gave Cursor an execution credential. The fixture checked that the scope/admin token variables were absent, approved the reviewed project server inside the launch, then replaced the child process with Cursor. A private observer retained the disposable execution token in memory to test revocation.

- The two-host exchange ran again, with Cursor and Codex each acting as sender, receiver, and task worker. In the Cursor-to-Codex direction the receipt linked `msg_18113441ac1ae71ceb69836b326e2df3` to `msg_90c39109b5d15780b4ea74027d264c3a`. In the other direction it linked `msg_b377a366931e2b21d03beaa13ad10c70` to `msg_13115d285f71b3dcb1e5cce22066b857`. All four tasks finished, and the owner resolved one escalation from each agent.
- On three distinct Cursor runs, the first check after the launcher returned found the former execution token rejected with `UNAUTHENTICATED` and the agent offline. The two HTTP checks took 4–5 ms after return. In the held-task case the agent had explicitly claimed the task and left it claimed. The owner found it open immediately after exit.
- With an older Cursor process holding a claim, a second launcher took the same agent ID and started Cursor's tool-list command. While the successor credential was live, the old token already returned `UNAUTHENTICATED` and the owner saw the task reopen. The old launcher exited with status 1. The successor listed 23 tools, exited successfully and then lost its own authority. The final session record still belonged to the successor.
- For each turn the observer searched the generated settings, captured stdout and stderr, and files in Cursor's test-project state directory for the literal scope, admin and current execution tokens. It found zero matches across 11, 12 and 13 files respectively.

## Retries and limits

- The first standalone approval outside a Git project could not find the project server. Running from the Git root found it. The sandbox initially blocked loopback access and the CLI's project-approval write. The successful checks used the real loopback daemon and Cursor's normal project approval store.
- Two follow-up attempts with a reduced environment reported authentication required before making any Bus call. Restoring the desktop-session variables let Cursor use its existing keyring. A lifecycle observer also stopped after a task claim when Fedora denied access to the host's process environment. That attempt was excluded from the lifecycle results. The replacement run checked the environment supplied at launch.
- The account rejected the explicit Composer 2.5 model before a tool call. Grok 4.7 Medium was available. In an initial launcher run, approval given before the launch left the MCP server unavailable to the model. Listing tools with an absolute executable alone also failed. Omitting the two forwarded variables left the bridge without an identity. Approving the reviewed entry from inside the launcher made it available.
- This was an existing workstation running a development build. Print mode, explicit pull delivery and the recorded CLI builds bound these observations. Editor sessions, interactive tool cancellation followed by another call, and other operating systems need separate runs. The earlier exchange used Auto, the peer's underlying model was undisclosed, and independent review is pending.

The private bundle produced with `scripts/verification-bundle.mjs` contains the prompts, event streams, configuration, observer checks and retry history. Its sanitized `run.log` is 598490 bytes with SHA-256 `d0975d24e16668d379e807c8845ce821651486741e9ffecac5db0a79e576a235`. The bundle digest check passed locally. Publication and independent review remain pending.
