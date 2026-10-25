# Portable scope archives

Portable scope archives move durable collaboration state between compatible Agent Intercom implementations. The archive format is JSON and is defined by [scope-archive.schema.json](schemas/scope-archive.schema.json).

An archive preserves:

- the scope ID and creation time;
- stable agent identities, capabilities, and peer links;
- messages, context, correlations, idempotency keys, receipts, and terminal delivery state;
- shared tasks, dependencies, ownership of completed tasks, and progress history;
- human escalations and answers;
- Agent Card publication identities;
- A2A tasks and their Bus message correlations;
- output streams, publisher assignments, retained values, and cursors.

An archive never contains scope tokens, agent tokens, scoped credentials, credential hashes, execution IDs, leases, reservations, rate-limit counters, or host process evidence.

## Safe restore state

Export requires every current agent execution to be offline, expired, or otherwise stopped. A reservation is stored as `queued` when it has never been delivered and `delivered` when it represents a redelivery. A claimed task is stored as `open`. These rules prevent an execution lease or transient claim from moving to another runtime.

Imported agents are offline and receive no usable agent credential. Registering an imported agent creates a new execution and credential. Imported Agent Card publications are disabled. Their owner can review and enable them after restore.

The event log is local projection history and is not portable. Import creates a new event stream containing one `scope.imported` event. Event consumers must rebuild their projection from the restored resources.

Scoped A2A and output credentials are not portable. A2A principal IDs are replaced by stable archive-only identifiers so task and message relationships remain intact without moving authority. Imported A2A history cannot be invoked by a new credential. Create new principals after import. Output values written by an old principal retain its opaque producer ID as historical attribution, but that ID grants no authority.

## Atomic and idempotent import

An implementation must validate the complete archive before applying it. Unsupported versions, unknown fields, broken references, malformed values, and conflicting IDs must fail without changing durable state.

Import is atomic. Retrying the exact same archive returns the existing scope without adding records. The new scope token is returned only when the import is first applied. Operators must save it immediately.

The reference runtime retains the metadata-only import marker so retries remain idempotent after normal event retention.

The initial archive identifier is:

```text
format: october-bus.scope
version: 2
```

New archive versions require an explicit reader implementation. Readers must reject versions they do not support.

## Transfer limits and full-database recovery

The reference runtime validates every successful export with its importer and checks its encoded size. Portable HTTP imports remain limited to 64 MiB. Exports apply a conservative payload/record-overhead budget before materializing state and may return `BACKPRESSURE` below that limit. Empty collections, including an output stream's publisher IDs, serialize as arrays, never null.

For larger retained state, the reference-runtime extension `GET /v1/admin/backup` streams a consistent SQLite snapshot rather than a JSON envelope. It requires admin authority and has a five-minute server deadline. This is a full-database disaster-recovery snapshot, not a portable scope archive: it includes credential hashes, executions, leases, all scopes, and event history. Protect it as sensitive data. See [operations](../../docs/operations.md) for offline restore and rollback.

## Channel history

Version 3 adds `channels`, `channelSubscriptions` and `channelPosts`. Roots and
replies retain their IDs, author attribution and retry keys. Subscriptions remain
bound to restored offline agent identities and grant no execution authority.
Import validates every channel, subscriber, post author and same-channel root
relationship before writing. Version 2 archives without channel data remain
accepted. Native wake attempts and provider socket bindings are not portable.

Version 3 also includes optional `attachments`. Each record contains its file
metadata, upload retry identity and complete base64 bytes. Import verifies size,
SHA-256, author attribution and retry hash before writing. Bytes are flushed
before the metadata transaction commits. A failed import may leave unreferenced
private files, collected later; it cannot commit a partial attachment record.
Deleted files and service transfer counters are not portable. Existing message
references to deleted files remain unchanged. The 64 MiB archive limit includes
encoded file bytes; a larger dataset needs a database-and-files backup.
