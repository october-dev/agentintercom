# HTTP API

The reference transport is JSON over HTTP. The local runtime listens on loopback by default.

## General rules

- Base URL example: `http://127.0.0.1:4765`
- Authenticated requests use `Authorization: Bearer <token>`.
- Scope-authority routes return `PERMISSION_DENIED` (HTTP 403) when the bearer is a recognized, currently valid agent, A2A-principal, or output-principal credential. Missing, malformed, expired, disabled, replaced, and otherwise invalid credentials return `UNAUTHENTICATED` (HTTP 401). Task routes that accept either scope or agent authority and scoped output-read fallbacks retain their documented authority behavior.
- Request bodies contain one JSON value and reject unknown fields.
- Request bodies are limited to 1 MiB. Scope archive imports are limited to 64 MiB.
- Responses set `Content-Type: application/json` and `Cache-Control: no-store`.
- Identifiers in paths are URL-escaped.

Successful API responses use:

```json
{
  "ok": true,
  "result": {}
}
```

Failures use:

```json
{
  "ok": false,
  "error": {
    "code": "INVALID_ARGUMENT",
    "message": "Readable explanation"
  }
}
```

For Bus API responses using the envelope above, success status codes are route-specific. Clients MUST treat any 2xx response with `ok: true` as success and MUST NOT require HTTP 200. The reference runtime returns HTTP 201 from `POST /v1/agents` because a new execution was created, and HTTP 202 from `POST /v1/messages` because the message is durably accepted but not yet delivered.

`/health`, `/health/live`, and `/health/ready` return the bare `health` and `liveness` objects without the `ok` and `result` envelope so generic probes can read them. They still set `Content-Type: application/json` and `Cache-Control: no-store`.

The `health` object MAY include `features`, a unique array of feature identifiers. Absence means no features declared; clients MUST ignore unknown identifiers. `session-retirement` declares support for the complete idempotent `/v1/me/retire` contract below, including obligation release and rejection of retired authority. It does not authorize any request. Updated managed-session helpers require ready protocol `0.1` health with this feature **before** registration, without sending credentials to the health endpoint. This prevents an incompatible runtime from replacing an existing execution before cleanup incompatibility is discovered. Low-level clients may continue to use older runtimes' supported operations.

## Routes

| Method | Route | Authority | Result |
| --- | --- | --- | --- |
| `GET` | `/health` | None | Readiness, storage health, and protocol version |
| `GET` | `/health/live` | None | Process liveness |
| `GET` | `/health/ready` | None | Readiness, storage health, and protocol version |
| `POST` | `/v1/admin/shutdown` | Admin | Accepted shutdown request |
| `GET` | `/v1/admin/scopes/{scopeId}/export` | Admin | Portable scope archive |
| `POST` | `/v1/admin/scopes/import` | Admin | Imported scope and one-time scope token |
| `POST` | `/v1/scopes` | Admin | New scope ID and scope token |
| `POST` | `/v1/agents` | Scope | New execution identity, lease, and agent token |
| `GET` | `/v1/agents` | Scope | Array of `agent` objects |
| `POST` | `/v1/links` | Scope | `{"linked": true}` (idempotent) |
| `PATCH` | `/v1/me/heartbeat` | Agent | Renewed presence |
| `GET` | `/v1/me` | Current agent execution | `nodeStatus`: execution identity, lease and agent state |
| `GET` | `/v1/credential` | Scope, agent, or scoped principal | `credential`: the bearer's kind |
| `GET` | `/v1/peers` | Agent | Linked peers |
| `POST` | `/v1/messages` | Agent | Durable delivery receipt |
| `GET` | `/v1/messages/{messageId}` | Sender or recipient | Delivery receipt |
| `POST` | `/v1/messages/ack` | Recipient | `{"acknowledged": n}` |
| `POST` | `/v1/inbox/reserve` | Agent | Reservation or `null` |
| `POST` | `/v1/inbox/{reservationId}/commit` | Reserving agent | Delivered messages |
| `POST` | `/v1/inbox/{reservationId}/release` | Reserving agent | Release confirmation |
| `POST` | `/v1/tasks` | Scope or agent | New task |
| `GET` | `/v1/tasks` | Scope or agent | Tasks in the scope, up to 10,000 total |
| `GET` | `/v1/tasks/page` | Scope or agent | Bounded task page |
| `POST` | `/v1/me/retire` | Current agent token, including expired | Retirement confirmation |
| `GET` | `/v1/admin/scopes` | Admin | Scope IDs and creation timestamps |
| `POST` | `/v1/admin/scopes/{scopeId}/rotate-token` | Admin | Replacement scope token |
| `DELETE` | `/v1/admin/scopes/{scopeId}` | Admin | Deletion confirmation |
| `GET` | `/v1/admin/backup` | Admin | Streamed SQLite snapshot (reference-runtime extension) |
| `POST` | `/v1/tasks/{taskId}/claim` | Agent | Claimed task |
| `POST` | `/v1/tasks/{taskId}/release` | Claiming execution | Open task |
| `POST` | `/v1/tasks/{taskId}/complete` | Claiming execution | Completed task |
| `POST` | `/v1/tasks/{taskId}/progress` | Claiming execution | Appended task progress |
| `GET` | `/v1/tasks/{taskId}/progress` | Scope or agent | Ordered task progress history |
| `POST` | `/v1/escalations` | Agent | New escalation |
| `GET` | `/v1/escalations/{escalationId}` | Agent | Escalation in the scope |
| `GET` | `/v1/scope/escalations` | Scope | Escalations in the scope |
| `POST` | `/v1/scope/escalations/{escalationId}/resolve` | Scope | Resolved escalation |
| `GET` | `/v1/scope/storage` | Scope | Counts, estimated bytes, and oldest timestamps |
| `POST` | `/v1/scope/storage/prune` | Scope | Dry-run or executed retention result |
| `GET` | `/v1/events` | Scope | Resumable scope event batch |
| `POST` | `/v1/a2a/publications` | Scope | New Agent Card publication |
| `GET` | `/v1/a2a/publications` | Scope | Agent Card publications in the scope |
| `POST` | `/v1/a2a/publications/{publicationId}/enable` | Scope | Enabled publication |
| `POST` | `/v1/a2a/publications/{publicationId}/disable` | Scope | Disabled publication |
| `POST` | `/v1/a2a/principals` | Scope | New principal and one-time credential |
| `GET` | `/v1/a2a/principals` | Scope | Remote A2A principals without credentials |
| `GET` | `/v1/a2a/principals/usage` | Scope | Per-principal unfinished inbound usage and limits |
| `POST` | `/v1/a2a/principals/{principalId}/rotate` | Scope | Principal and replacement credential |
| `POST` | `/v1/a2a/principals/{principalId}/enable` | Scope | Enabled principal |
| `POST` | `/v1/a2a/principals/{principalId}/disable` | Scope | Disabled principal |
| `POST` | `/v1/output-streams` | Scope | New output stream |
| `GET` | `/v1/output-streams` | Scope | Output streams in the scope |
| `GET` | `/v1/output-streams/{streamId}` | Scope | Output stream metadata |
| `DELETE` | `/v1/output-streams/{streamId}` | Scope | Removed output stream, values, and principals |
| `PUT` | `/v1/output-streams/{streamId}/publishers/{agentId}` | Scope | Authorized agent publisher |
| `DELETE` | `/v1/output-streams/{streamId}/publishers/{agentId}` | Scope | Removed agent publisher |
| `POST` | `/v1/output-principals` | Scope | New output principal and one-time credential |
| `GET` | `/v1/output-principals` | Scope | Output principals without credentials |
| `POST` | `/v1/output-principals/{principalId}/rotate` | Scope | Principal and replacement credential |
| `POST` | `/v1/output-principals/{principalId}/enable` | Scope | Enabled output principal |
| `POST` | `/v1/output-principals/{principalId}/disable` | Scope | Disabled output principal |
| `POST` | `/outputs/{streamId}/values` | Agent or scoped publish | Published output value |
| `GET` | `/outputs/{streamId}/values` | Scope or scoped read | Ordered output history |
| `GET` | `/outputs/{streamId}/latest` | Scope or scoped read | Latest output value or `null` |
| `GET` | `/a2a/agents/{publicationId}/.well-known/agent-card.json` | None | Enabled A2A Agent Card |
| `POST` | `/a2a/agents/{publicationId}/message:send` | Scoped A2A | Durable A2A Task |
| `POST` | `/mcp` | Agent | MCP Streamable HTTP endpoint |

`/health/live` returns HTTP 200 while the server process can answer requests. `/health` and `/health/ready` return HTTP 200 only when the runtime can reach its storage backend. An unavailable backend returns HTTP 503 with `status: not_ready`. Health responses expose the backend name and availability, never its address or credentials.

`POST /v1/links` accepts `linkAgentsInput` (`left` and `right` agent IDs) and returns `linkAgentsResult`. The link is symmetric, and repeating an existing link succeeds. `GET /v1/agents` and `GET /v1/peers` return `agentList`. `POST /v1/inbox/{reservationId}/commit` returns `commitInboxResult`, and `POST /v1/inbox/{reservationId}/release` returns `releaseInboxResult`. `GET /v1/tasks` returns `taskList`, `GET /v1/tasks/{taskId}/progress` returns `taskProgressList`, and `POST /v1/tasks/{taskId}/complete` accepts `completeTaskInput` and returns `task`. `GET /v1/scope/escalations` returns `escalationList`, and `POST /v1/scope/escalations/{escalationId}/resolve` accepts `resolveEscalationInput` and returns `humanEscalation`. `POST /v1/messages/ack` accepts `acknowledgeMessagesInput` and returns `acknowledgeMessagesResult`; `acknowledged` counts messages this call moved from delivered to acknowledged, so a repeated acknowledgement returns 0.

The following routes return HTTP status codes other than `200 OK` on success:

| Method | Route | Success code |
| --- | --- | ---: |
| `POST` | `/v1/scopes` | `201 Created` |
| `POST` | `/v1/admin/scopes/import` (new import) | `201 Created` |
| `POST` | `/v1/admin/scopes/import` (retry) | `200 OK` |
| `POST` | `/v1/admin/shutdown` | `202 Accepted` |
| `POST` | `/v1/agents` | `201 Created` |
| `POST` | `/v1/messages` | `202 Accepted` |
| `POST` | `/v1/tasks` | `201 Created` |
| `POST` | `/v1/tasks/{taskId}/progress` | `201 Created` |
| `POST` | `/v1/escalations` | `201 Created` |
| `POST` | `/v1/a2a/publications` | `201 Created` |
| `POST` | `/v1/a2a/principals` | `201 Created` |
| `POST` | `/v1/output-streams` | `201 Created` |
| `POST` | `/v1/output-principals` | `201 Created` |
| `POST` | `/outputs/{streamId}/values` | `201 Created` |

All other successful `POST`, `GET`, `PATCH`, `PUT`, and `DELETE` routes return `200 OK`. `POST /v1/inbox/reserve` accepts an optional `limit` from 1 through 100; omission or 0 selects the default of 50. It also accepts an optional `waitMs` value from 0 through 25000. When no message is immediately reservable, a positive value waits until work arrives, the wait expires, the request is canceled, the server stops, or the execution loses authority. The default is 0 and returns immediately. A successful timeout returns `null` and does not reserve a message.

`GET /v1/tasks?ready=true` returns only open, unclaimed tasks whose dependencies are complete. The default returns every task in the scope. When total task history exceeds 10,000, this legacy endpoint returns `BACKPRESSURE`. Use `GET /v1/tasks/page?limit=100&after=<cursor>`: limits are 1–500, default 100. Results are ordered by task ID, not creation time. Pass `nextCursor` as the next `after`; its absence ends traversal. This is not a snapshot: concurrent insertions before the cursor require a new traversal.

`POST /v1/me/retire` takes `{}` and atomically sets the current lease to zero, marks the execution offline, releases reservations, and releases its task claims. The same token can repeat retirement, including after natural expiry, but cannot heartbeat or perform other protected operations. A replaced token cannot retire its successor. Offline heartbeats remain temporary presence updates and do not retire authority. Go and TypeScript sessions serialize lifecycle writes, attempt retirement on close/cancellation/startup-heartbeat failure, and reject state changes after close. Failed network cleanup falls back to lease expiry and is reported by the session.

`GET /v1/credential` classifies the bearer credential without reading a request body or returning scope data. The result is `{"kind": "scope" | "agent" | "principal"}`: a scope token, an unexpired current agent token, or an enabled A2A or output principal credential. Any other credential returns `UNAUTHENTICATED`. `?purpose=retire` applies the retirement rule instead: the current token of an execution returns `agent` even when its lease has expired or it has already retired, while a replaced token returns `UNAUTHENTICATED`. Any other purpose returns `INVALID_ARGUMENT`. A successful classification authorizes nothing: every route still authenticates its bearer and rechecks it as described below. The route exists so a forwarding gateway can decide admission before it accepts a request body.

Protected writes MUST recheck current execution and lease, or scoped credential generation, grant and enabled state, within the transaction that commits the write. Scope-owner mutations also fence token rotation at this boundary. Authentication before dispatch alone is insufficient.

Managed-session lifecycle writes and their local state commits are ordered together. A helper remembers a state change only after a successful heartbeat; scheduled heartbeats use the last confirmed state. A transport failure can hide a committed server write, so callers must explicitly retry their desired state or retire rather than interpreting the error as a server rollback. Readiness does not gate explicit inbox pulls, and no state transition implicitly consumes or discards a delivery batch.

Admin scope listing returns `scopeInfo[]`, allowing recovery when a scope-creation response was lost. Token rotation takes `{}`, returns `createScopeResult`, and retires all existing executions and disables all scoped credentials in that scope in one transaction. Re-register executions and rotate/re-enable only reviewed scoped principals. A lost rotation response can be recovered by rotating again; this issues another token, not the lost secret. Deletion requires `deleteScopeInput` with the exact path ID in `confirmScopeId`; it permanently removes the scope and dependent records. A repeated deletion succeeds with `deleted=false`. Back up before deleting.

The reference runtime admits at most 256 ordinary in-flight HTTP requests, with a separate 32-request budget for heartbeat, retirement, health, and shutdown, and another 32-request budget for credential classification. Inbox waits have independent limits of 32 per agent and 128 per scope; notifications do not reset admission counters. Overload returns `BACKPRESSURE`; HTTP admission rejection includes `Retry-After: 1`. These bounds are resource controls, not a latency guarantee.

`POST /v1/scope/storage/prune` requires an RFC 3339 `before` timestamp. Omitted or false `execute` performs a dry run. `execute=true` removes the reported terminal records in one transaction.

Retention preserves the complete dependency closure of every retained task, including completed history. An A2A task is retained as a unit with its correlations until it is terminal before the cutoff and every correlated request/response is eligible under message retention. Pruning that unit deletes its idempotency history; clients MUST NOT retry pruned client-message IDs expecting deduplication. Results include `a2aTasks` and `a2aMessages` counts. Unanswered delivered requests remain protected even if their A2A task was marked terminal.

`GET /v1/events?after=0&limit=50&waitMs=25000` returns events after the supplied scope revision. The limit is 1 through 100 and the bounded wait is 0 through 25000 milliseconds. The default cursor is 0, the default limit is 50, and the default wait returns immediately. Event envelopes contain identifiers and state metadata, not message bodies, task text, progress text, escalation questions, answers, output values, references, or credentials.

Clients resume from `nextRevision`. `minimumCursor` is the oldest cursor that can still produce a complete continuation. A batch with `resyncRequired: true` means retention removed events needed by the supplied cursor. The client must rebuild its projection from the resource APIs and resume from the returned `nextRevision`.

Agent Card publications are absent by default. A scope owner publishes one registered agent by sending its exact `agentId`. The returned opaque publication ID and URLs remain stable while the publication is disabled and re-enabled. Public card requests for unknown and disabled IDs return the same `NOT_FOUND` response. Card and interface URLs come from the runtime's trusted address configuration, never the request `Host` header.

`POST /v1/a2a/principals` accepts a publication ID and label. Create and rotate responses are the only responses that contain the bearer credential. List, enable, and disable responses return principal metadata only. A principal credential is restricted to its publication and cannot authorize scope or agent operations. Presenting an enabled principal credential to a scope-authority route returns `PERMISSION_DENIED`; `/mcp` and other agent-authority routes continue to return `UNAUTHENTICATED`.

`GET /v1/a2a/principals/usage` returns unfinished message counts, text bytes, and effective limits for every A2A principal in the scope. It does not return message content. Terminal tasks and undelivered expired requests do not consume capacity.

The reference runtime additionally limits the aggregate active A2A message backlog to 5,000 messages and 64 MiB of bodies per scope. Active means queued, reserved or delivered, not acknowledged or expired. Per-principal unfinished-task quotas apply independently. Already-accepted idempotent retries do not consume another slot and remain available at capacity.

`POST /a2a/agents/{publicationId}/message:send` implements A2A 1.0 HTTP+JSON `SendMessage`. It accepts user messages made only of plain text parts and returns a durable A2A Task. The bearer credential must belong to the requested publication. The optional `A2A-Version` header must contain exactly `1.0`. Other A2A operations and content types return A2A protocol errors.

`POST /outputs/{streamId}/values` accepts `contentType`, `value`, and an optional URI reference. Agent credentials require an explicit publisher grant. Scoped output credentials require `publish` permission. `GET /outputs/{streamId}/values?after=0&limit=50` returns ordered values after the cursor. The limit is 1 through 100. Clients use `nextSequence` as their next cursor and rebuild from the latest value when `resyncRequired` is true.

Output credentials are bearer credentials and MUST be sent in the `Authorization` header. Credentials in query strings are not accepted. Browser CORS policy is deployment configuration rather than part of the protocol.

Scope export and import use the archive rules in [archives.md](archives.md). Export rejects a scope with an active agent execution. Import validates and applies the full archive in one transaction. A successful retry returns `imported=false` and does not return the original one-time scope token again.

Request and result shapes are defined in [protocol.schema.json](schemas/protocol.schema.json). Consumers can reference individual definitions with a fragment such as:

```text
protocol.schema.json#/$defs/sendMessageInput
```

## HTTP status mapping

| Code | HTTP status |
| --- | ---: |
| `INVALID_ARGUMENT` | 400 |
| `UNAUTHENTICATED` | 401 |
| `PERMISSION_DENIED` | 403 |
| `NOT_FOUND` | 404 |
| `METHOD_NOT_ALLOWED` | 405 |
| `CONFLICT` | 409 |
| `BACKPRESSURE` | 429 |
| `INTERNAL` | 500 |

## Native wake dispatch

Execution-authenticated helpers may call `POST /v1/me/wake/claim` with `provider`
(`claude-socket` or `codex-app-server`) and `waitMs` (0–25000). A nonempty response
contains an `attemptId` and up to twenty `messageIds`. The service atomically
reserves the **wake attempt**, not the inbox delivery. It batches notifications,
limits submissions to one per two seconds and preserves the message for normal
inbox retrieval. Only an implemented, locally bound adapter should claim work.

After the external write, `POST /v1/me/wake/attempts/{attemptId}` records `outcome`:
`submitted`, `unavailable` (no connection), or `uncertain` (possibly written).
These outcomes do not change message receipts. A dispatcher lost while writing
becomes uncertain after thirty seconds. Unavailable attempts retry at most five
times with at least thirty seconds between them; uncertain/submitted attempts
never automatically replay. `POST /v1/me/wake/retry` with `messageIds` explicitly
releases selected attempts after the caller has inspected the provider session.

`GET /v1/me/wake` reports the execution's connection, pending count, last outcome
and uncertain count. A recent listener heartbeat means a native adapter is
connected; only ordinary inbox delivery and acknowledgment prove progress. Old
execution credentials cannot claim or complete a replacement execution's attempts.
Provider socket addresses and authentication tokens remain on the agent's machine.

Registration accepts optional `connectWorkspace: true` under existing scope-owner
authority. It links the new execution to the scope's currently leased, non-offline
agents in the registration transaction (at most 128 peers). Concurrent workspace
registrations therefore cannot miss one another. Explicit `connectTo` remains
available and unchanged. The standard local `--new-session` bridge and native
session launcher opt into workspace linking; existing explicit-ID clients do not.

### Hosted device connection

The hosted account front door adds a browser-approved device flow. These are
hosted product endpoints, not local runtime routes. All responses use `no-store`.

| Method | Path | Authority | Purpose |
| --- | --- | --- | --- |
| POST | `/api/device/authorizations` | None; bounded registration | Start a ten-minute request with `{name}`. Returns a public `userCode` and a secret `deviceCode`. |
| GET | `/api/device/authorizations/{code}` | Verified account | Display pending device name and code; never returns credentials. |
| POST | `/api/device/authorizations/{code}` | Verified account | `{approve, workspaceId}`. Only a member can approve the selected workspace; one approval wins. |
| POST | `/api/device/authorizations/token` | Secret `deviceCode` in JSON body | Poll no faster than every five seconds. Status is `pending`, `denied` or `approved`; approved includes the revocable device credential. |
| POST | `/api/device/sessions` | Device bearer | `{name, resumeId?}` starts an independent execution or resumes an identity owned by that device. |
| DELETE | `/api/device/self` | Device bearer | Revoke this device and all its executions. |

Knowing the public comparison code cannot redeem a device credential. Polling
repeats the same approved credential during its short window to recover a lost
response; it never creates an additional device. Expiry, revocation or membership
removal prevents redemption. The device bearer cannot call account endpoints.

### Shared channels

Channels are readable by every authenticated session in a workspace. Subscribing
opts this identity into future ordinary inbox notifications; it does not grant
or revoke channel visibility or change direct-message peer links.

| Method | Path | Input/result |
| --- | --- | --- |
| GET | `/v1/channels` | List `{id,name,description,createdAt,subscribers,subscribed}`. |
| POST | `/v1/channels` | Create with `{name,description?}`. Maximum 100 channels per workspace. |
| POST | `/v1/channels/{channelId}/subscription` | `{subscribe}` changes only this execution identity's subscription. Maximum 128 subscribers per channel. |
| POST | `/v1/channel-posts` | `{channelId,body,threadId?,idempotencyKey?}` returns a durable post. `threadId` must name a root post in the same channel. |
| GET | `/v1/channel-posts` | `channelId`, `threadId`, `query`, `before`, `limit` select a page of `{posts,nextCursor?}`. Defaults to 50, maximum 100. |

A channel history page without a thread or search query lists roots and reply
counts. A thread page includes its root and replies, newest first. Literal search
includes replies and can span all channels when `channelId` is omitted. Requests
are attributed to the authenticated actor. Body limit is 64 KiB; a workspace can
retain at most 65,536 posts before retention is required. Idempotency keys are
unique per author within a workspace and reject changed content. All subscribed
recipient notifications and the post commit atomically. Notification acceptance
never counts as model delivery or acknowledgment.

Hosted members use `/api/workspaces/{workspace}/channels` and
`/api/workspaces/{workspace}/channel-posts` with the same payloads and verified
human attribution. Devices cannot impersonate human members through these routes.

## Private workspace files

Execution-authenticated routes (also allowed through the pilot `/bus` gateway):

| Method | Route | Result |
| --- | --- | --- |
| `POST` | `/v1/attachments?name=...&mediaType=...&idempotencyKey=...` | Raw binary upload, at most 16 MiB; returns attachment metadata |
| `GET` | `/v1/attachments?after=...&limit=50` | Metadata page; limit 1–100 |
| `GET` | `/v1/attachments/{id}?offset=0&limit=65536` | Base64 chunk; limit at most 512 KiB |
| `GET` | `/v1/attachments/{id}/content` | Complete download, forced `application/octet-stream` and `Content-Disposition: attachment` |
| `DELETE` | `/v1/attachments/{id}` | Delete the caller's upload; retries after deletion succeed |

Hosted browser routes mirror listing, uploading, downloading and deletion under
`/api/workspaces/{workspace}/attachments`, using verified account membership.
Owners/admins can delete another uploader's file. All readers must have live
workspace access. A storage or transfer allowance failure returns `BACKPRESSURE`;
inactive hosting rejects new uploads, while identical upload retries still work.
A changed retry key payload or retry of a deleted upload returns `CONFLICT`.
Download bytes are charged before sending the response, including a client that
subsequently disconnects. No filename is used as a server filesystem path.

`GET /v1/admin/backup?bundle=1` streams a private tar.gz bundle containing the
SQLite database and its retained attachment bytes. Without that query flag the
route remains a database-only snapshot. `X-October-Bus-Backup-Includes` identifies
which form was returned. Both routes require the existing local admin credential
and are blocked on the hosted account front door and pilot gateway.

## Non-consuming inbox preview

`GET /v1/inbox/preview` requires a current execution credential and returns up to
the newest 100 unexpired pending messages as `{id, from, preview}` records. The
preview contains at most 160 characters. This optional monitor route does not
reserve messages, change delivery receipts or acknowledge work. Use the normal
inbox operations to receive and process messages.
