# State machines

This document gives the explicit states and transitions of protocol 0.1 objects: executions, message delivery,
shared tasks, and human escalations. It complements the prose rules in the [protocol profile](README.md); where the two
differ, open an issue. Every transition is described by public operation, credential, error code, and scope event, so
an implementation can reproduce it without the reference runtime.

Each table lists:

- **From → To**: the state change. `—` means the object does not exist yet.
- **Trigger**: the public operation, or *materialization* for time-based changes. A time-based change takes effect
  when a later operation observes the object, not at a wall-clock instant, and is reported when it is applied.
- **Credential**: who may cause it (see [Authority](README.md#authority)).
- **Event**: the [scope event](README.md#scope-events) type recorded with the change.

Errors that apply to every agent operation are not repeated per row: a replaced or unknown agent token returns
`UNAUTHENTICATED`, and an expired lease returns `UNAUTHENTICATED` for every operation except retirement.

Items marked **Open** describe current reference-runtime behavior that has not yet been confirmed as the intended
protocol rule.

## Execution

An agent ID keeps its identity across executions. Each registration creates a new execution with its own token and
lease; at most one execution per agent ID is current.

```text
      register                heartbeat (renews lease)
 — ─────────────▶ current ◀──────────┐
                   │ │ │ └───────────┘
                   │ │ └─ lease passes without renewal ──▶ expired
                   │ └─── retire, or scope token rotation ─▶ retired
                   └───── register the same agent ID ──────▶ replaced

 expired ─── retire, or scope token rotation ───▶ retired
 current, expired, or retired ─── register the same agent ID ───▶ replaced
```

| From → To | Trigger | Credential | Effect | Error | Event |
| --- | --- | --- | --- | --- | --- |
| — → current | `POST /v1/agents` | Scope | New execution ID and agent token; lifecycle `starting`, `ready=false`. Releases the agent ID's inbox reservations and any claims whose execution is no longer current. | `INVALID_ARGUMENT` for an invalid ID, lease, or capabilities | `agent.registered`; `link.created` for each new link requested with `connectTo` |
| current, expired, or retired → replaced | `POST /v1/agents` with the same agent ID | Scope | The previous token stops authorizing requests immediately, including heartbeat and retirement. | — | `agent.registered` (for the new execution) |
| current → current | `PATCH /v1/me/heartbeat` | Agent | Renews the lease and records lifecycle and readiness. | `INVALID_ARGUMENT` for `lifecycle=offline` with `ready=true` | `agent.lifecycle_changed` when lifecycle or readiness changed |
| current → expired | *materialization* when the lease end passes | — | The execution is unreachable and cannot act. Its claims are released lazily (see [Shared task](#shared-task)). | — | none for the execution itself |
| current or expired → retired | `POST /v1/me/retire` | Agent (the same execution) | Lifecycle `offline`, `ready=false`, lease ended; inbox reservations and claims are released in the same transaction. Retiring again succeeds. | `UNAUTHENTICATED` for a replaced token | `agent.lifecycle_changed` with reason `retired` |
| current or expired → retired (every execution in the scope) | `POST /v1/admin/scopes/{scopeId}/rotate-token` | Admin | Issues a new scope token. In one transaction, every execution in the scope becomes `offline` with its lease ended, their inbox reservations and task claims are released, and scoped credentials are disabled. | `NOT_FOUND` for an unknown scope | `scope.credentials_rotated` (one event for the scope) |

**Lifecycle** (`starting`, `ready`, `working`, `idle`, `needs_input`, `offline`) is reported state, not a machine
enforced by the runtime: a heartbeat may move between any two values. The only constraint is that `offline` requires
`ready=false`. `reachable` is derived: the lease is current and lifecycle is not `offline`.

## Message delivery

```text
          reserve                commit                 acknowledge
 queued ───────────▶ reserved ───────────▶ delivered ─────────────▶ acknowledged
   ▲                   │  ▲                  │
   └── release, or ────┘  └─── reserve ──────┘
       reservation            (redelivery)
       expires

 queued, reserved, or delivered ─── message expiry ───▶ expired
```

A released or expired reservation returns each message to the state it had before it was reserved: `queued`, or
`delivered` for a redelivery.

| From → To | Trigger | Credential | Effect | Error | Event |
| --- | --- | --- | --- | --- | --- |
| — → queued | `POST /v1/messages` | Agent or scoped A2A principal (sender) | Durable acceptance; not delivery. | For an agent sender, `PERMISSION_DENIED` when the recipient is not a linked peer (an unknown agent is never linked). For an A2A principal sender, `NOT_FOUND` for an unknown recipient agent. `CONFLICT` when an idempotency key is reused with different content; `BACKPRESSURE` at the scope message limit; response-link errors below | `message.accepted`; for a response, also `message.replied` on the linked request |
| queued → reserved | `POST /v1/inbox/reserve` | Agent (recipient) | Reservation with an expiry (30 seconds in the reference runtime); other reservations cannot take the message. | — (a timeout returns no reservation) | `message.reserved` |
| delivered → reserved | `POST /v1/inbox/reserve` | Agent (recipient) | Redelivery of a delivered message that was never acknowledged. | — | `message.reserved` |
| reserved → delivered | `POST /v1/inbox/{reservationId}/commit` | Agent (the reserving execution) | Messages are handed to the caller; the first delivery time is kept on redelivery. | `NOT_FOUND` for an unknown reservation; `CONFLICT` for an expired one (the reservation is released first) | `message.delivered` |
| reserved → queued or delivered | `POST /v1/inbox/{reservationId}/release`, or reservation expiry at *materialization* | Agent (the reserving execution) or — | Returns each message to its state before the reservation. Releasing an unknown or already released reservation succeeds and changes nothing. | — | `message.released` |
| delivered → acknowledged | `POST /v1/messages/ack` | Agent (recipient) | Terminal. Acknowledging again is not an error and is not counted. | — | `message.acknowledged` |
| queued, reserved, or delivered → expired | *materialization* after `expiresInMs` passes | — | Terminal; the message can no longer be delivered. | — | `message.expired` |

Response linking, checked when a `response` is sent:

| Condition | Error |
| --- | --- |
| `responseTo` missing on a response, or present on another mode | `INVALID_ARGUMENT` |
| No request with that ID between the reversed participants | `NOT_FOUND` |
| The request expired before delivery | `CONFLICT` |
| The request has not been delivered | `CONFLICT` |
| The request already has a response | `CONFLICT` |

A request delivered before it expired may still receive its one response.

## Shared task

```text
        claim               complete
 open ───────────▶ claimed ───────────▶ done
   ▲                  │
   └── release, or ───┘
       the claiming execution
       is no longer current
```

| From → To | Trigger | Credential | Effect | Error | Event |
| --- | --- | --- | --- | --- | --- |
| — → open | `POST /v1/tasks` or the `add_task` tool | Scope or agent | Dependencies must already exist in the scope. | `NOT_FOUND` for an unknown dependency; `BACKPRESSURE` at the scope's active task limit | `task.created` |
| open → claimed | claim | Agent | The claim belongs to the current execution, not only the agent ID. | `NOT_FOUND` for an unknown task; `CONFLICT` when the task is not open, a dependency is not done, or another claim won | `task.claimed` |
| claimed → done | complete | Agent (the claiming execution) | Terminal; an optional note is stored. | `NOT_FOUND` for an unknown task; `CONFLICT` when not claimed by this execution (**Open**: see below) | `task.completed` |
| claimed → open | release | Agent (the claiming execution) | The task becomes claimable again. | `NOT_FOUND` for an unknown task; `CONFLICT` when not claimed by this execution (**Open**) | `task.released` with reason `released` |
| claimed → open | *materialization* when the claiming execution is no longer current (lease expired, replaced, or retired) | — | Applied no later than the next task listing or claim in the scope. The reference runtime also applies it on registration, retirement, scope token rotation, and storage summary and prune. | — | `task.released` with reason `execution_expired` (**Open**) |

A task becomes *ready* when it is `open` and every dependency is `done`. There is no transition out of `done`.

## Human escalation

```text
      create               resolve
 — ───────────▶ pending ───────────▶ resolved
```

| From → To | Trigger | Credential | Effect | Error | Event |
| --- | --- | --- | --- | --- | --- |
| — → pending | `POST /v1/escalations` | Agent | Question with no options or two to four options. | `INVALID_ARGUMENT` for one option or more than four; `BACKPRESSURE` at the per-agent or per-scope pending limit | `escalation.created` |
| pending → resolved | `POST /v1/scope/escalations/{escalationId}/resolve` | Scope | Terminal; stores the answer. | `NOT_FOUND` for an unknown escalation; `CONFLICT` when the escalation is already resolved | `escalation.resolved` |

## Bounded context

Context items have no states. They are immutable parts of the message that carries them and share its delivery state.

## Open questions

These describe the reference runtime's current behavior. They are not yet protocol rules; implementations should not
depend on them until each is settled in [#28](https://github.com/october-dev/october-bus/issues/28).

1. **Escalation answers.** The answer is not checked against the escalation's options.
2. **Task ownership errors.** Completing or releasing a task claimed by another agent or execution returns `CONFLICT`,
   not `PERMISSION_DENIED`.
3. **Release reasons.** Claims freed by retirement, replacement, or token rotation are reported with reason
   `execution_expired`, the same as lease expiry. Clients cannot distinguish them from the event.
