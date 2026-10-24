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
