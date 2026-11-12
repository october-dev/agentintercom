# Cross-machine scope architecture

Status: accepted for protocol 0.1

Agent Intercom supports local and cross-machine coordination with one protocol. A scope has one authoritative Bus service for its durable coordination state. Each machine remains authoritative for the agent executions it runs.

## Deployment shapes

### Local

One daemon stores the scope in SQLite and serves local agents through HTTP or MCP.

```text
local agents
     |
Agent Intercom
     |
   SQLite
```

This remains the default and requires no remote service.

### Shared service

Several hosts connect to one authorized Bus endpoint backed by shared durable storage.

```text
host A agents ----\
                   Agent Intercom service ---- durable storage
host B agents ----/
```

The service owns logical scope state. It does not own the processes on either host.

### Remote agent boundary

An agent can also be exposed through its A2A interface. A2A is the preferred boundary when the remote participant does not need membership in the same Bus scope.

## Authority model

The Bus service owns:

- scope membership and peer links;
- durable messages, receipts, tasks, dependencies, and escalations;
- logical presence, execution leases, and event ordering;
- authentication and authorization for protocol operations.

The host or adapter owns:

- process identity and lifecycle evidence;
- terminal, foreground, and input state;
- safe delivery into a running harness;
- credentials and permissions that must remain on the host.

Only the host that runs an execution can report its physical state. A remote service cannot infer that a terminal is safe, inject input by itself, or transfer execution authority to another machine.

## State ownership

Protocol 0.1 uses one writer for each logical scope. Messages, tasks, escalations, publications, and the scope event sequence are committed by that authority.

Clients may reconnect to another service instance only when every instance uses the same transactional storage backend. The instances are stateless protocol servers over one authoritative database. They are not independent replicas.

Local caches and projections are allowed, but they are never authoritative. Clients rebuild them from resource APIs and resumable scope events.

## Failure and recovery

- An accepted mutation is successful only after its transaction commits.
- Retrying an idempotent mutation returns the original result.
- A host that disconnects stops renewing its execution lease.
- Expired execution authority releases work according to the protocol rules.
- A reconnecting host registers a new execution and cannot reuse the old authority.
- Database backup, migration, and failover must preserve transaction ordering and constraints.
- Network partitions do not create a second writable copy of a scope.

## Security boundary

Remote deployments require authenticated endpoints and transport security. Agent credentials grant only their documented scope and execution permissions. Storage credentials remain inside the Bus service and are never given to agents.

Execution credentials and physical host evidence stay with the owning host. Messages and context do not grant access to local tools, files, terminals, or accounts.

## Replication

Multi-primary scope replication is not part of protocol 0.1. It would require conflict rules for messages, task claims, leases, credentials, retention, and event revisions. Adding it before a concrete deployment requires those semantics would weaken durability and execution authority.

A future replication profile must be optional, preserve one authoritative outcome for every mutation, and pass reconnect and partition conformance tests. Local and shared-service deployments must not depend on it.

## Implementation sequence

1. Keep SQLite as the local default.
2. Separate protocol behavior from the storage implementation.
3. add a transactional PostgreSQL backend for shared services.
4. add portable deployment profiles without making a provider part of the protocol.
5. add reconnect, failover, and concurrent-writer conformance checks.
