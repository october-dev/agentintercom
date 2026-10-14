# October Bus protocol 0.1

Status: draft

October Bus is a communication and coordination protocol for AI agents and harnesses. It defines identity, discovery, durable messages, shared tasks, and human escalation. It does not select agents, staff teams, route models, or supervise an overall operation.

## Model

```text
scope
├── agent identity
│   └── current execution and lease
├── authorized peer links
├── durable messages and receipts
├── shared tasks and dependencies
├── human escalations
└── portable durable state
```

### Scope

A scope is one collaboration boundary. Agents, peer links, messages, tasks, and escalations never cross scopes.

A scope token MAY register agents, list all agents, create peer links, add and list shared tasks, and resolve human escalations. It MUST NOT be given to an agent process when an execution-bound agent token is sufficient.

### Agent and execution

An agent ID is the stable programmatic identity of one participant inside a scope. It MUST match:

```text
^[A-Za-z0-9][A-Za-z0-9_.:-]{0,127}$
```

Agent IDs are byte-exact and case-sensitive. `Reviewer` and `reviewer` are different agents.

Each registration creates a new execution ID and agent token. Registering the same agent ID replaces its current execution. The previous token MUST stop authorizing requests.

An execution has a renewable lease between 30 seconds and 24 hours. The default is five minutes. An expired execution is unreachable and cannot act. A launcher SHOULD heartbeat at roughly one third of the lease duration.

### Presence and lifecycle

Reachability, readiness, and lifecycle are separate facts:

- `reachable` means the execution lease is current and the execution is not offline;
- `ready` means the adapter has evidence that the agent can accept work;
- `lifecycle` is one of `starting`, `ready`, `working`, `idle`, `needs_input`, or `offline`.

An adapter MUST report only states it can prove. A generic process launcher MAY report reachability but SHOULD leave `ready=false` until a host signal proves readiness.

An `offline` heartbeat MUST set `ready=false`.

Reporting `ready=true` is the host's promise that it can accept and act on queued
deliveries. A host that has signaled readiness MUST promptly resume its inbox
consumer so deliveries that were queued before readiness become visible without
waiting for a fresh message arrival. Queued work is not pushed to the host and
must not be consumed on the host's behalf: the runtime keeps every message in the
inbox until the host's own reservation returns it. The runtime wakes a blocked
inbox reservation on the false→true ready edge so the host's in-flight wait
re-checks promptly; the returned batch always belongs to the host's consumer. A
host MUST NOT issue a reservation and discard the result in order to "drain" its
inbox; doing so would consume delivery attempts without delivering work.

Offline presence does not end the execution lease. Retirement is a separate idempotent operation that ends authority and releases claims and inbox reservations transactionally. A retired token MUST NOT renew its lease. A replaced token MUST NOT retire its successor. Managed sessions MUST serialize lifecycle writes with retirement and attempt cleanup when startup fails after successful registration.

### Capabilities

An agent MAY declare up to 64 capabilities. Capability names use the agent-ID character rules. Names MUST be unique within one declaration. Descriptions are optional and limited to 512 bytes.

Capabilities describe what an agent claims it can do. They do not grant authority.

## Peer discovery and addressing

Agents communicate only across explicit peer links. A link is symmetric.

Protocol clients MUST address a peer by its exact agent ID. An interactive adapter MAY accept a display name as a convenience if it applies these rules in order:

1. resolve a byte-exact agent ID first;
2. otherwise resolve a case-insensitive exact display name only when it identifies one linked peer;
3. reject duplicate exact display names with `CONFLICT`;
4. reject unknown and partial names without guessing.

A peer can remain discoverable while offline. Clients MUST inspect `reachable` and `ready` instead of treating discovery as availability.

## Messages

Message modes are `notify`, `request`, and `response`. An omitted mode means `notify`.

A successful send means the runtime durably accepted the message. It does not mean the recipient read or processed it.

Every message identifies the kind and ID of its sender and recipient. Protocol 0.1 supports registered agents and scoped A2A principals. Agent-to-agent messages still require an explicit peer link. An A2A principal can address only the agent named by its publication.

Delivery states are:

```text
queued -> reserved -> delivered -> acknowledged
   ▲         │   ▲         │
   └─release─┘   └─reserve─┘ (redelivery of an unacknowledged message)

queued, reserved, or delivered -> expired
```

[State machines](state-machines.md) lists every transition with its trigger, credential, errors, and event.

Reservations prevent two concurrent delivery attempts from consuming the same inbox item. A reservation expires after 30 seconds in the reference runtime. Releasing or expiring a reservation makes an undelivered message available again. Delivered but unacknowledged messages MAY be redelivered.

An inbox reservation request MAY wait for work with `waitMs`. The reference profile allows values from 0 through 25000. The wait ends when work becomes reservable, its deadline passes, the caller cancels, the server stops, or the execution loses authority. A timeout returns no reservation and does not consume work.

Acknowledgement means the recipient reports that it processed the message. Clients SHOULD acknowledge only after processing succeeds.

### Idempotency

An idempotency key is scoped to one sender inside one scope. Retrying the same logical send with the same key and identical content MUST return the original receipt. Reusing the key with different content MUST return `CONFLICT`.

An idempotency key remains bound to its message for as long as that message is retained. SDKs SHOULD generate a new UUID for every logical send and MUST NOT intentionally recycle keys. An operator's retention cutoff MUST be longer than every client retry window it needs to support.

### Requests and responses

A response MUST name one delivered request in `responseTo`. The response sender and recipient MUST be the reverse of the request. One request accepts at most one response.

A request that expires before delivery cannot receive a response. A request delivered before expiry MAY receive one response after expiry. Its receipt then reports both expiry and the linked response.

### Expiry and durability

`expiresInMs=0` means no protocol expiry. A positive expiry can be at most 30 days. Expiry is materialized before delivery, reservation release, reservation commit, and receipt reads. An expired message cannot be delivered or resurrected.

The reference runtime limits each scope to 10,000 messages that are neither acknowledged nor expired. It returns `BACKPRESSURE` when the limit is reached. Operators remain responsible for diagnostics and retention policy.

### Bounded context

A message MAY include up to 32 context items and 256 KiB of total context metadata and content. Supported kinds are `text`, `file`, `url`, and `reference`.

A context item is a description or explicit payload. It does not grant access to a file, URL, tool, account, or host.

## Shared tasks

A task has a short title and an optional description. It is `open`, `claimed`, or `done`, and MAY depend on existing tasks in the same scope. Scope authority and agents can add and list tasks. A task created by scope authority has a null `createdBy` value.

An open task is ready when it is unclaimed and every dependency is done. Clients MAY request only ready work when listing tasks.

An agent can claim an open task only after every dependency is done. A claim belongs to the current execution, not only the logical agent. Only that execution can release or complete it.

The claiming execution MAY append ordered `progress`, `note`, or `blocker` entries while the task is claimed. Other agents and scope authority can read the history. Entries remain available after completion and are removed only when explicit retention removes their task.

The reference runtime limits progress text to 4,000 bytes, history to 1,000 entries, and total progress text to 1 MiB per task. Task results include the most recent 20 entries. The full bounded history is available through the progress-list operation.

The claimant MUST keep its execution lease current. When an execution expires or is replaced, the reference runtime lazily releases its stale claims so another execution can claim them.

The reference runtime limits a scope to 5,000 tasks that are not done.

## Portable scope state

Compatible implementations MAY export and import durable scope state using the versioned [portable archive format](archives.md). Archives preserve accepted work and stable resource IDs while excluding credentials, leases, reservations, execution authority, and host evidence.

## Storage and retention

Accepted work is retained indefinitely by default. Scope authority MAY inspect record counts, estimated payload bytes, and oldest state timestamps without reading record content.

Explicit retention can remove only terminal records older than a caller-provided cutoff. It MUST preserve active delivery, reply, task, and human obligations. A request and response MUST be removed together, and only after both are terminal. A delivered request without a response remains eligible for a late response and MUST NOT be removed. A completed task MUST NOT be removed while an unfinished task depends on it. Removing a task also removes its progress history.

Retention MUST support a dry run and report exact record counts. The reference CLI requires `--yes` before deletion.

## Scope events

Scope authority MAY follow a durable event log to update external projections. Each event has an opaque ID and a scope-local, monotonically increasing revision. Clients resume by requesting events after the last revision they committed.

Events describe state changes without carrying protected record content. Message bodies and context, task titles, descriptions and progress text, escalation questions and answers, output values and references, and credentials MUST NOT appear in an event envelope.

Protocol 0.1 defines these event types:

- `agent.registered` and `agent.lifecycle_changed`
- `link.created`
- `message.accepted`, `message.replied`, `message.reserved`, `message.released`, `message.delivered`, `message.acknowledged`, and `message.expired`
- `task.created`, `task.claimed`, `task.released`, `task.completed`, and `task.progress_added`
- `escalation.created` and `escalation.resolved`
- `a2a.publication_created`, `a2a.publication_enabled`, and `a2a.publication_disabled`
- `a2a.task_created`, `a2a.message_accepted`, and `a2a.task_state_changed`
- `credential.created`, `credential.rotated`, `credential.enabled`, and `credential.disabled`
- `output.stream_created`, `output.stream_removed`, `output.publisher_added`, `output.publisher_removed`, and `output.published`
- `scope.imported` and `scope.credentials_rotated`

Each listed transition event MUST be committed atomically with the transition it describes. Retrying an idempotent operation that made no new state change MUST NOT append another event. A heartbeat that only renews a lease does not append a lifecycle event.

Implementations MAY retain a bounded event history. `minimumCursor` identifies the oldest cursor that can still produce a complete continuation. When retention removes revisions required by a cursor, the event API MUST return an explicit resync result instead of silently skipping history. The client then rebuilds from the resource APIs and resumes at the current revision.

The reference runtime allows at most 128 concurrent event waits per scope credential and returns `BACKPRESSURE` above that limit. Waiting clients do not hold mutation locks or private in-memory event queues.

## A2A Agent Card publications

No registered agent is publicly discoverable by default. Scope authority MAY create one durable Agent Card publication for a registered agent. The publication receives an opaque public ID that does not contain its scope or agent ID.

An enabled publication exposes an A2A Agent Card at its returned card URL. The card uses the agent's display name and declared capabilities, but MUST NOT expose scope IDs, agent IDs, execution IDs, credentials, prompts, local paths, or private context. Public callers cannot list publications. Unknown and disabled publication IDs are indistinguishable.

Scope authority MAY disable and re-enable a publication without changing its ID or URLs. Card and interface URLs MUST be derived from trusted runtime configuration instead of request headers. The reference runtime permits loopback HTTP and requires HTTPS for non-loopback publication URLs.

## Scoped A2A credentials

A scope owner MAY create remote principals for an Agent Card publication. Each principal has a stable opaque ID, a human-readable label, and one bearer credential restricted to invoking that publication. The plaintext credential is returned only when the principal is created or rotated. It MUST NOT appear in lists, Agent Cards, events, logs, or errors.

Rotating a principal invalidates its previous credential immediately without changing the principal ID. Disabling a principal suspends its current credential, and re-enabling it restores that credential unless it was rotated. Disabling the publication also prevents its principals from authenticating.

A scoped A2A credential grants no access to the Bus HTTP API, MCP endpoint, scope authority, agent authority, other publications, or daemon administration. Implementations MUST store a one-way digest instead of the plaintext credential and compare presented credentials in constant time.

### Durable A2A task correlation

An A2A Task and an October Bus shared task are different resources. An A2A Task represents one remote delegated interaction. A Bus shared task is work that any eligible agent can claim.

The bridge stores each A2A task, its context ID, remote principal, publication, target agent, state, and message correlations. Every accepted A2A client message maps to one Bus request. That request can have at most one Bus response. A client can add the next message while the task is `input-required`, without weakening the Bus one-response rule.

A2A task states are `submitted`, `working`, `input-required`, `completed`, `failed`, `canceled`, and `rejected`. Delivery of the first pending Bus request advances `submitted` to `working`. A Bus response completes the task unless it is waiting for input. An undelivered request that expires fails the task. Terminal tasks are immutable.

Client message IDs are idempotent within one remote principal. Repeating an accepted message returns its existing task and Bus correlation. Reusing the ID with different content returns `CONFLICT`. A principal cannot read or change another principal's task records.

The reference HTTP+JSON binding accepts authenticated A2A `SendMessage` requests containing plain text parts. It joins multiple parts in order with a newline, creates a durable Bus request, and returns the correlated A2A Task. The first binding does not accept file, URL, data, extension, referenced-task, streaming, push-notification, task-read, task-list, or cancellation operations. Rejected requests do not create Bus work. If an `A2A-Version` header is present, it MUST contain exactly `1.0`.

The reference runtime limits unfinished inbound work independently for each A2A principal. The defaults are 1,000 messages and 16 MiB of text. A limit check and message acceptance occur in the same transaction. Terminal tasks and undelivered expired requests release their capacity. Scope authority can inspect each principal's counts, bytes, and effective limits without reading message content.

## Output streams

A scope owner MAY create a named output stream for values consumed by websites, dashboards, automations, and other tools. A stream has an opaque ID, a scope-unique name, a retention limit, a monotonically increasing sequence, and an explicit set of agent publishers.

An authorized agent or scoped output principal MAY publish `text/plain` or `application/json` values. Each value records its sequence, producer type, producer ID, content type, creation time, and an optional absolute URI reference. Text values are limited to 64 KiB. Encoded JSON values are limited to 256 KiB. References contain no fetched content and do not expand authority.

The latest value and ordered history are readable with scope authority or a scoped output principal with `read` permission. A principal can hold `read`, `publish`, or both permissions for exactly one stream. The credential has no access to messages, context, tasks, agents, MCP, administration, or another stream.

Output principal credentials follow the same one-time issuance, secure digest storage, rotation, and enable or disable rules as scoped A2A credentials. Listing principals never returns their credentials. Removing a stream also removes its values and principals.

History cursors use stream sequence numbers. When bounded retention removes values required by an old cursor, the result sets `resyncRequired` and supplies the current sequence. Output values are retained independently of the scope event log. Each publication also appends a metadata-only `output.published` event so scope event followers can react without receiving the output value itself.

The reference runtime retains 1,000 values by default and permits a configured limit from 1 through 10,000. It limits each publishing identity to 120 publications per minute and each scoped reader to 600 reads per minute. Scope-owner reads are not rate limited. A scope can contain at most 1,000 output streams and a stream can authorize at most 128 agent publishers.

## Human escalation

An agent MAY create an escalation with a question and either no options or two to four options. Creating an escalation does not grant the agent permission or answer the question.

Only scope authority resolves escalations. Agent authority can create and read escalations in its scope but cannot resolve them. The reference runtime limits pending escalations to 100 per agent and 1,000 per scope.

## Authority

| Credential | Allowed operations |
| --- | --- |
| Admin token | Create, export, and import scopes and request local daemon shutdown |
| Scope token | Register and list agents, create peer links, manage Agent Card publications and remote principals, manage output streams and readers, add and list tasks, follow scope events, inspect and prune storage, list and resolve escalations |
| Agent token | Heartbeat, discover peers, message linked peers, use inboxes, coordinate tasks, publish to explicitly allowed output streams, create and read escalations |
| Scoped A2A credential | Invoke one published A2A agent interface when that operation is supported |
| Scoped output credential | Read or publish one output stream according to its explicit permissions |

Tokens are bearer credentials. Implementations MUST compare them safely, MUST NOT log them, and MUST reject empty credentials. Agent tokens MUST stop working after lease expiry or execution replacement.

Messages do not expand authority. A recipient MUST still apply its own permissions before using tools or changing external state.

## Errors

Protocol error codes are:

- `INVALID_ARGUMENT`
- `UNAUTHENTICATED`
- `PERMISSION_DENIED`
- `NOT_FOUND`
- `METHOD_NOT_ALLOWED`
- `CONFLICT`
- `BACKPRESSURE`
- `INTERNAL`

Clients SHOULD branch on the code, not the human-readable message.

## Limits and ordering

Text limits in the reference runtime are measured in UTF-8 bytes. JSON Schema string limits are a portable approximation because JSON Schema measures characters.

The reference runtime returns peers by registration time, tasks by creation time, and messages by creation time. Clients MUST NOT use this ordering as a fairness or scheduling guarantee.

Protocol 0.1 has no pagination, extension negotiation, or mixed-version negotiation. Implementations MUST reject unsupported protocol versions rather than silently changing semantics.

## Profiles

### Local runtime profile

Requires the HTTP API, durable persistence, loopback binding by default, all authority rules, and every semantic in this document.

### MCP adapter profile

Requires the local runtime profile plus the tool mapping in [mcp.md](mcp.md). Registration, token handoff, heartbeat, and cleanup remain adapter responsibilities outside the model loop.

### Native adapter profile

Uses the same semantics through public APIs and MAY add host hooks for stronger lifecycle and wake evidence. Native hooks cannot weaken authority, durability, or acknowledgement rules.

## Related documents

- [State machines](state-machines.md)
- [HTTP API](http.md)
- [MCP mapping](mcp.md)
- [Adapter contract](adapters.md)
- [JSON Schemas](schemas/protocol.schema.json)
