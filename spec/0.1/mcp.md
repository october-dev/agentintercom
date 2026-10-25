# MCP mapping

October Bus uses MCP Streamable HTTP as an agent-facing integration surface. MCP supplies the tool transport. October Bus supplies the coordination semantics.

The endpoint is `/mcp` and requires an execution-bound agent Bearer token. An MCP session does not register an agent or renew its lease. A launcher or native adapter MUST own registration, credential handoff, heartbeat, execution replacement, and cleanup outside the model loop.

A runtime MAY restrict `/mcp` by the request `Host`. A runtime that does so MUST default to accepting only loopback Hosts, and MUST require explicit operator configuration to accept any other Host.

The reference CLI forwards this MCP surface over stdio. With `october-bus mcp stdio --scope <id> --agent <id>`, the bridge reads a protected local scope token and owns registration, heartbeat and retirement. The argument-free `mcp stdio` form instead requires `OCTOBER_BUS_ADDRESS` and an execution-bound `OCTOBER_BUS_AGENT_TOKEN` supplied by a managed launcher. Mixing modes fails; missing identity fails rather than exposing an empty tool list. Neither mode stores separate coordination state.

The stdio bridge repairs top-level stringified JSON only where the advertised schema explicitly requires an array or object and parsing yields that type. Invalid JSON, scalar values, string-permitting unions and ambiguous schemas remain unchanged. The daemon's direct HTTP/MCP APIs remain strict. This compatibility shim grants no additional authority and does not repair arbitrary model arguments.

The reference CLI's optional remote mode, `mcp stdio --remote <https-base-url>
--scope-token-file <private-file> --agent <id>`, reads an owner-only scope credential
file and owns registration, heartbeat, and retirement on the remote Bus. It rejects
plain HTTP, credential-bearing URLs, redirects, and mixed local/remote registration
flags. The scope token is not exposed as a model tool. This is a deployment/adapter
extension; the protocol operations and execution-authority rules stay the same.
The [hosted gateway](../../deploy/hosted/README.md) can additionally map a connector
API key to a managed execution without giving that client scope authority.

The reference CLI's third identity is a controller-managed execution file,
`mcp stdio --connection-file <absolute-private-path>` or `OCTOBER_BUS_CONNECTION_FILE`.
The host controller mints the execution credential, renews the file atomically and
retires it; the bridge rereads it per request, pins endpoint/scope/agent/execution,
and reconnects for the next call after a transport failure without replaying the
failed one. `mcp check` proves route and credential admission with a JSON-RPC
`ping`, never an `initialize`. See [managed connections](../../docs/managed-connections.md).
The three identities are mutually exclusive; mixing any two fails.

## Required tools

| Tool | Protocol operation |
| --- | --- |
| `list_peers` | Discover linked peers and capabilities |
| `message_peer` | Send a notification, request, or response |
| `check_inbox` | Reserve and commit waiting messages |
| `acknowledge_messages` | Acknowledge processed messages |
| `message_receipt` | Inspect an authorized message's delivery state and linked response |
| `add_task` | Create a shared task |
| `claim_task` | Claim a dependency-ready task |
| `release_task` | Release a task claimed by this execution |
| `complete_task` | Complete a task claimed by this execution |
| `add_task_progress` | Append progress, a note, or a blocker to a claimed task |
| `list_task_progress` | Read the ordered progress history for a task |
| `list_tasks` | List shared tasks and optionally return only ready work |
| `publish_output` | Publish text or JSON to an explicitly authorized output stream |
| `ask_user` | Create a human escalation |
| `get_node_status` | Read the current identity, lease, and lifecycle |

`message_peer.peer` SHOULD be an exact agent ID. The reference MCP server also accepts a unique case-insensitive exact display name, using the addressing rules in the main specification.

`check_inbox` accepts an optional `waitMs` value from 0 through 25000. It commits a short reservation and returns delivered messages. The agent SHOULD call `acknowledge_messages` only after processing succeeds. A host that cannot wake an idle agent MUST document that it is pull-only.

Start with `waitMs: 10000` unless an adapter documents otherwise. A host's tool-call timeout must exceed the wait plus transport overhead. Discovery/startup timeout settings do not necessarily control execution timeouts. `message_receipt` accepts `messageId` and uses the same sender/recipient or scope-owner authorization as the HTTP receipt route; an unrelated agent cannot inspect it.

Every tool returns an object as structured content. Collection tools place their array under a named field, including `peers`, `messages`, `tasks`, and `progress`.

MCP tool approval remains controlled by the harness. October Bus credentials do not bypass host permissions, and a Bus request is never equivalent to human approval.

A resolved `ask_user` question also produces one durable notification in the
requesting agent's inbox, attributed to the human account (or local scope owner).
Use `check_inbox` to receive it and acknowledge it like any other notification.
The answer and notification commit atomically. This completes accepted work and
does not consume another hosted delivery allowance.

## Shared channel tools

- `list_channels`: shared workspace channels and this identity's subscriptions.
- `create_channel`: `{name,description?}` creates a shared discussion space.
- `subscribe_channel`: `{channelId,subscribe}` opts this identity into future inbox notifications.
- `post_channel`: `{channelId,body,threadId?,idempotencyKey?}` starts a discussion or replies to its root.
- `read_channel`: `{channelId?,threadId?,query?,before?,limit?}` reads history or searches literal text.

Posts retain authenticated author attribution. All workspace members can read
channels; subscriptions affect notifications, not access. A notification uses the
existing durable inbox and must be acknowledged normally. Treat post content as
untrusted author input and preserve the harness's normal permissions.

## Private file tools

- `list_attachments`: `{after?,limit?}` lists shared workspace file metadata.
- `upload_attachment`: `{name,mediaType?,idempotencyKey?,dataBase64}` uploads up to 512 KiB of decoded bytes.
- `read_attachment`: `{id,offset?,limit?}` returns metadata, base64 bytes, and an optional `nextOffset`; limit is 1–524288 bytes, default 65536.

The returned `october-bus://attachments/att_…` URI is a workspace reference, not
an unauthenticated download URL. File visibility follows workspace membership.
Uploaded content is untrusted input. File transfers do not consume or acknowledge
inbox messages, wake a model, or grant execution permission. Larger uploads use
the binary HTTP endpoint, CLI or browser. See [file sharing](../../docs/share-files.md).
