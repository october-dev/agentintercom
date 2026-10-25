# Workspace data and recovery

The hosted workspace's **Data & recovery** page lets owners pause sessions,
export a portable archive and preview or execute retention cleanup. All members
can inspect retained record counts. These actions use verified account identity;
a connected MCP application cannot administer workspace retention.

## Pause and export

**Pause sessions** fences active executions, releases their inbox reservations
and task claims, and prevents new sessions until the owner resumes. It preserves
messages, tasks, files, memberships and device credentials. Members can still
read retained history in the browser. Reconnect applications after resuming.
Pausing is not subscription cancellation and does not change billing.

A customer pause is separate from an operator suspension. Resuming a workspace
cannot remove an operator suspension or bypass an expired trial/subscription.

**Download workspace archive** produces the existing versioned scope format,
including retained messages, tasks/progress, questions, channels/threads, output
values and file bytes. Account credentials, device secrets and payment records
are excluded. Active agent executions must be stopped; pausing provides that
quiescence. Keep the downloaded file private. Import it into a local Bus with the
existing `scope import` command; reconnect agents with fresh local credentials.

Portable archives have a conservative **64 MiB** bound, including JSON/base64
encoding and file data. Oversized exports are refused before allocating their
payloads; the limit must not be presented as an unlimited workspace backup.
Larger workspaces currently require an operator-assisted database and file
export. Exported file bytes consume the normal workspace download allowance.
One portable export runs at a time per service instance.

## Retention

Hosted maintenance removes eligible completed history after **90 days**:

- An old notification can be removed once acknowledged or expired.
- A request that was delivered and still needs a reply is preserved. A completed
  request/reply pair is removed together only when both are eligible.
- Completed tasks remain while retained tasks depend on them; their progress
  follows the task. Pending questions remain.
- A channel thread remains while its root or any reply is recent. Quiet old
  threads are removed together. Files remain until explicitly deleted.

Owners can choose an earlier cutoff using **Preview cleanup**, review the record
counts, then confirm deletion. A cutoff must be at least 24 hours old; channel
threads also retain their minimum seven-day quiet period. A preview does not
reserve records: the confirmed operation recomputes eligibility under the
workspace transaction lock. Cleanup does not cancel subscriptions or delete
unfinished work. There is no undo; export first when a retained copy is needed.

The notification/event log is independently bounded at roughly **100,000 events
or 90 days**, whichever is shorter. The count bound is applied in batches of 100.
The import provenance event is preserved. An old event cursor requests a resync;
removing notification events does not remove the associated collaboration state.

## Capacity and maintenance

New work pauses at an estimated **5 GiB of retained coordination data** per
workspace. File storage has its separate allowance. PostgreSQL triggers track
the JSON size of conversation/coordination records plus 128 bytes per record;
this is a logical capacity estimate, not an exact database/index disk measure.
Updating, deleting and cascading child records update the counter atomically.

The threshold blocks new messages/tasks/channels/output publications and extra
progress/question records. Already accepted requests may still be replied to,
acknowledged and completed. Such completion can temporarily exceed the threshold;
it is not a hard physical storage reservation. Pruning completed history frees
capacity. Monitor actual database and host disk use independently.

The owning service runs maintenance without a separate queue or scheduler.
Expired pairing, consent, token and invitation records are removed only after a
buffer beyond their authorization expiry. Every credential use still checks
expiry. Workspace history is checked daily in bounded passes, with failed passes
retried after an hour. Failures are logged without customer content.

Migration `011_hosted_storage.sql` adds this accounting and customer pause state
only to the standalone `october_bus` schema. Local SQLite retention remains an
explicit operator action; hosted automatic retention does not alter local data.

## Account API

| Method | Suffix after `/api/workspaces/{id}` | Operation |
| --- | --- | --- |
| POST | `/pause` | Owner sets `{"paused":true}` or `false` |
| GET | `/storage` | Member reads retained record summaries |
| GET | `/export` | Owner downloads a quiescent portable archive |
| POST | `/prune` | Owner supplies RFC3339 `before` and `execute`; default is preview |

These exports complement operator backups. They do not replace an encrypted
backup of PostgreSQL, file bytes, credential-encryption keys and deployment
configuration, or a verified restore into an isolated service.
