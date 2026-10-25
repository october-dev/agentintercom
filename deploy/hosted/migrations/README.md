# Hosted Bus database migrations

These migrations own the standalone runtime's `october_bus` schema. They do not
modify existing application schemas. Account and billing changes owned by another
service stay in that service's migration history.

## Deploy

Use PostgreSQL 17 or later with a UTF-8 database. Supply the connection URL through
`OCTOBER_BUS_DATABASE_URL` in an owner-only service environment file or secret
manager. Remote connections require `sslmode=verify-full` and a trusted CA.
Never pass the URL on the command line or commit it.

```sh
october-bus database migrate
october-bus database check
```

`migrate` takes a transaction advisory lock, verifies the recorded SHA-256 of each
embedded SQL file, and applies missing migrations in one transaction. Failure
rolls back the batch. It refuses edited or unknown migrations. Service startup
only checks this history and never runs DDL. Use a migration identity authorized
to create the schema and revoke its public grants. A separate runtime identity
needs schema USAGE and SELECT/INSERT/UPDATE/DELETE on the schema's tables, but no
DDL privileges. Keep both identities outside the public API roles.

The migration runner revokes PUBLIC and existing Supabase `anon`/`authenticated`
access. Do not expose this schema through PostgREST. Workspace authorization is
performed by the Bus service; a database connection is service authority.

## Initial schema: 001_runtime.sql

Creates the runtime's scopes, agents, messages, reservations, tasks, events,
escalations, scoped credentials, A2A correlations, output streams and indexes.
It is additive to an existing PostgreSQL database, but expects no independently
created tables in `october_bus`. Existing SQLite databases remain local and use
their own migration history. Transfer quiescent scopes through export/import.

After deployment, `database check` verifies every filename and checksum. Confirm
readiness reports `storage.backend=postgres`; run the public conformance profile
on an isolated test database. Do not run conformance against customer workspaces.

## Backup and recovery

Use PostgreSQL backups (`pg_dump` with a matching/newer client or provider-managed
backups), including the migration ledger. SQLite's `backup` endpoint is not a
PostgreSQL backup. Encrypt backup files, retain them outside the service host,
and verify an isolated restore before production migration. Stop writers during
cutover and confirm readiness, message round trips and receipt integrity after
restore. A deployment binary must match the restored migration history.

Never edit an applied SQL file or automatically drop a schema on rollback. An
additive migration can usually stay in place while the application is repaired;
if the previous binary refuses the newer history, restore the matching backup
with writers stopped. Destructive migrations require their own export, restore
and compatibility procedure.

## Local verification

Create a disposable loopback database named `october_bus_test`, then set
`OCTOBER_BUS_TEST_POSTGRES_URL` and run:

```sh
go test ./bus ./conformance -run TestPostgres -count=1
```

These tests apply migrations and create synthetic scopes. They refuse remote
hosts and other database names. Without this environment variable they skip.

## Workspace schema: 002_workspaces.sql

Adds standalone hosted workspaces, memberships, invitations, devices, session
ownership and delivery counters. References the runtime's scope and agent tables.
User IDs are verified external identities; this migration neither grants access
to existing application data nor creates an Auth database dependency. Encrypted
service credentials require the separately backed-up hosted encryption key.

## MCP authorization: 003_oauth.sql and 004_oauth_client_auth.sql

Adds OAuth client registrations, expiring consent requests and encrypted grants
bound to a single workspace connection. Access, refresh and authorization-code
lookups use SHA-256 digests. Client secrets are stored only as digests. Refresh
rotation, code redemption and connection revocation take the same workspace
transaction lock as session changes. Migration 004 adds confidential client
methods; existing public registrations retain the `none` method.

## Human attribution: 005_human_messages.sql

Extends message participant constraints with `human` and indexes workspace
history. No message rows are rewritten, and the existing agent receipt lifecycle
is unchanged. Human identity and access come from the hosted member boundary.

## Native dispatch: 006_native_wake.sql

Adds operational wake connection and attempt state. Socket paths, provider tokens
and transcript content are not stored. The same portable SQL creates SQLite's
wake tables in schema 11. Message foreign keys remove attempts when retained
messages are pruned; scope deletion also cascades. Portable scope archives omit
this execution-specific state, just as they omit live execution credentials.

## Device approval: 007_device_pairing.sql

Adds ten-minute CLI pairing requests, random device-code digests and encrypted
approved credentials. Browser approval is bound to a verified member and one
workspace. Conditional approval prevents concurrent requests from issuing two
devices; polling permits lost-response recovery within the same approval window.
Expired requests are removed on subsequent registration, with a bounded table.

## Shared channels: 008_channels.sql

Adds shared channels, per-agent notification subscriptions and threaded posts.
The same SQL defines SQLite schema 12. Search text is normalized in Go so Unicode
lowercasing is consistent across storage backends. Post bodies are stored once;
fan-out uses compact notifications in the existing message ledger. Archive
version 3 includes channel content and subscriptions, and accepts old version 2
archives without channel records. Retention removes whole quiet threads after
both the requested cutoff and their seven-day notification lifetime.

`009_attachments.sql` adds private file metadata and monthly byte accounting.
SQLite schema 13 adds the same tables. File bytes are stored outside PostgreSQL:
configure `OCTOBER_BUS_ATTACHMENTS_DIR` on persistent private storage before
starting the hosted service. The supplied service unit creates that directory
under `/var/lib/october-bus-hosted`. Only one service may own its attachment
store. Back up both the database and files; a PostgreSQL dump alone is incomplete.

## Workspace billing: 010_billing.sql

Adds account trial windows, isolated workspace billing customers/subscriptions,
immutable checkout attempts, verified funded invoice periods and webhook receipts.
Existing workspace owners seed their first trial window from existing workspaces.
All tables remain in `october_bus`; no existing product billing schema is changed.
See [billing configuration](../../../docs/hosted-billing.md).

## Retained-data accounting: 011_hosted_storage.sql

Adds customer pause state, logical storage counters and maintenance timestamps.
Two child tables persist their parent scope for correct cascading-delete accounting;
triggers supply it for existing runtime inserts. Counter functions run with the
caller’s privileges and a fixed schema search path. No external Auth or product
ledger is modified. See [workspace data](../../../docs/workspace-data.md).

## Manual MCP keys: 012_mcp_keys.sql

Extends connection kinds with `mcp_key`. These credentials are accepted only by
the hosted MCP endpoint and identify one connector, unlike machine credentials
that can start separate sessions. Existing device/OAuth records are unchanged.
Manual keys use the existing digest, expiry, revocation and ownership columns;
no credential plaintext or new credential store is added.

## Workspace deletion: 013_workspace_deletion.sql

Persists deletion intent before external billing calls and retains minimal closed
workspace payment references independently of content. Requests revoke sessions
and pause the workspace; retries close pending checkouts and confirm subscription
cancellation before removing the scope. The same billing lock prevents a new
purchase racing deletion. Owner changes and connection replacement are refused
once deletion starts. Private file bytes are reclaimed by the orphan collector.

The optional [Supabase account integration](../supabase/README.md) is applied
separately by the identity administrator. It guards shared account deletion and
membership creation without granting the runtime privileges on Auth tables.
