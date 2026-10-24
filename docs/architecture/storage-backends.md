# Storage backend contract

October Bus keeps protocol behavior above its storage implementation. The runtime depends on a domain-level storage contract instead of issuing database queries from HTTP, MCP, or A2A handlers.

SQLite is the default local backend. It requires no external service. Hosted services can use PostgreSQL with the same domain transactions and public HTTP/MCP contract.

## Required behavior

Every backend must preserve the same observable semantics:

- accepted messages are durable before success is returned;
- idempotent retries return the original result;
- inbox reservation, delivery, and acknowledgement transitions are atomic;
- one request accepts at most one response;
- task claims belong to one current execution;
- dependencies are checked in the same transaction as task claims;
- scope events commit with the state change they describe;
- credentials are stored as one-way digests;
- retention preserves active delivery, reply, task, and escalation obligations;
- archive import applies completely or not at all.

A backend cannot weaken these rules to match the features of its database.

## Readiness

The runtime reports its backend name and whether the backend is reachable. Liveness and readiness are separate:

- `GET /health/live` reports whether the server process can answer requests;
- `GET /health/ready` reports whether protocol operations can reach storage;
- `GET /health` is the standard readiness endpoint.

Health responses never contain a database address or credential.

## PostgreSQL

PostgreSQL uses [versioned migrations](../../deploy/hosted/migrations/README.md). `OpenPostgres` checks schema checksums without changing the database. SQL placeholders and integer flags are adapted at the driver boundary; delivery, authority, retention and archive logic are shared with SQLite.

A transaction advisory lock serializes each scope across server processes. Different scopes can progress concurrently. Statements use READ COMMITTED snapshots after acquiring the lock, so token rotation, execution replacement, deduplication and task claims see preceding committed changes. Cross-process inbox/event waits check the database at most once per second while waiting; in-process commits also signal immediately. This works with transaction poolers without a separate notification service.

Tests cover concurrent independent runtimes and the same public conformance profile as SQLite. Set `OCTOBER_BUS_TEST_POSTGRES_URL` to run them against an isolated loopback database. Capacity still needs qualification for each deployment and workload.

Shared deployments may run several stateless Bus server instances over one PostgreSQL database. Independent databases are not replicas of one scope, and storage support does not grant a server authority over processes running on another host.

