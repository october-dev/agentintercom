# Agent Intercom Roadmap

Agent Intercom is an open communication and coordination layer for AI agents and harnesses.

This document describes the path to a stable, portable interoperability runtime.
It is a technical plan, not a release promise. The phases below describe capability
groups, not a requirement to migrate Desktop before shipping hosted workspaces.

## Current release work

The standalone source now includes SQLite and PostgreSQL storage, hosted accounts
and workspace membership, revocable machines and MCP connectors, OAuth and manual
keys, a browser workspace, channels/threads/search/files, billing and usage limits,
retention/export, encrypted backup tooling, and native Claude Code/Codex terminal
wake. Codex wake remains experimental. Native wake is separate from generic MCP.

Before offering the hosted service or publishing the next release:

- Qualify the actual deployment, account/email redirects, isolation and recovery.
- Configure off-host backups and verify a restore using the deployment identities.
- Exercise payment lifecycle behavior with the actual merchant account.
- Measure load, storage growth, resource limits and operational alerts on the host.
- Run clean-install/upgrade and platform checks, including the release adapter gate.
- Record named-client compatibility without inferring it from a generic MCP test.
- Complete code-owner review and all required release checks.

October Desktop convergence is deferred until the standalone release has been
used and validated in production. Its current implementation and backend ledger
remain independent. Larger self-service exports (currently bounded at 64 MiB)
and native wake for additional apps remain follow-up work.

## Goals

Agent Intercom should:

- run on Windows, macOS, and Linux;
- work without October Desktop;
- be simple to install as one native command;
- let agents discover, message, delegate, coordinate tasks, and ask a human;
- preserve durable delivery and execution-scoped authority;
- support local, cross-machine, and server-to-server operation;
- support self-hosted and managed remote endpoints through the same protocol;
- provide a clear path for any harness to become compatible;
- be directly usable by October without maintaining a separate Bus core.

Agent Intercom will not decide which agents to create, which models to use, how to staff a team, or how to judge an operation. Those decisions belong to products built above the protocol.

## Runtime architecture

The architecture is:

- a native Go daemon and reference runtime;
- a versioned, language-neutral protocol;
- an official MCP integration in the daemon;
- a TypeScript SDK for harnesses and October Desktop;
- additional SDKs based on contributor demand;
- SQLite for local durable state;
- PostgreSQL for hosted durable state, using the same runtime operations;
- HTTPS and authenticated MCP/HTTP for self-hosted and managed operation;
- black-box conformance tests that do not depend on an implementation language.

```text
Harness or agent
       |
Client SDK or MCP adapter
       |
Agent Intercom protocol
       |
Local daemon and SQLite
       |
Optional remote transport
       |
Self-hosted or managed endpoint
```

The daemon is the compatibility boundary. Desktop applications, terminal harnesses, and plugins should communicate with it through the public protocol instead of importing private runtime state.

### Why Go

Agent Intercom is infrastructure that should run quietly on developer machines and servers. Go is a strong fit because it can produce native binaries for the target platforms, has a small operational surface, supports long-running services well, and has an official MCP SDK.

A native daemon also prevents the Bus from inheriting the Node.js version of every harness or desktop application. TypeScript remains useful at the edges, where most harness integrations and October Desktop already live.

## Protocol principles

The stable protocol will follow these rules:

- Identity, readiness, reachability, and lifecycle are separate facts.
- Authority belongs to one execution and expires unless renewed.
- Agents communicate only inside an authorized scope.
- Context is explicit and bounded.
- Accepted messages are durable before success is returned.
- Delivery, acknowledgement, expiry, and reply completion are observable.
- Retries must not create duplicate work silently.
- A peer request cannot expand local permissions.
- Human escalation asks for authority but never invents it.
- Remote transport does not weaken local security rules.
- Optional capabilities must be declared and tested.

## Phase 0: Runtime foundation

Establish the native runtime and validate its platform boundaries before expanding the protocol.

### Deliverables

- One daemon command that starts and stops cleanly.
- Builds for Windows, macOS, and Linux.
- Builds for the primary x64 and arm64 targets where the operating system supports them.
- One durable SQLite message written before acknowledgement.
- Idempotency keys that make message retries safe.
- One MCP tool served through the official Go SDK.
- One TypeScript client calling the daemon.
- Owner-only local credentials and runtime files on each platform.
- Graceful recovery after a forced restart.
- Release artifact, checksum, and signing experiments.
- Measured binary size, idle memory, startup time, and message latency.

### Completion gate

The foundation is complete when it proves:

- reliable builds and tests on all three operating systems;
- a practical SQLite build without fragile platform setup;
- clean MCP support;
- predictable service lifecycle and local authentication;
- straightforward use from October Desktop and external harnesses;
- a simpler installation story than requiring a separate runtime.

Keep one reference runtime. Do not maintain competing Go and TypeScript daemons.

## Phase 1: Protocol foundation

Define the language-neutral contract before porting more code.

### Deliverables

- Versioned protocol document.
- Machine-readable request and response schemas.
- Stable identifiers for scopes, agents, executions, messages, tasks, and escalations.
- Error codes and retry guidance.
- Capability and lifecycle model.
- Delivery and reply state machines.
- Idempotency-key scope and lifetime.
- Execution-bound task claims and lease recovery.
- Bounds for messages, context, queues, reservations, and leases.
- Compatibility and deprecation policy.
- Initial threat model.

### Exit criteria

- The Go runtime and TypeScript client can run the same protocol fixtures.
- Invalid and unauthorized operations have deterministic results.
- A new SDK can be written from the protocol without reading daemon code.

## Phase 2: Local reference runtime

Implement the complete local core in the selected runtime.

### Deliverables

- Scope creation and local administration.
- Agent registration and execution replacement.
- Leases, heartbeat, readiness, and lifecycle.
- Explicit peer relationships and capability discovery.
- Durable notifications, requests, responses, and receipts.
- Inbox reservation, delivery, acknowledgement, retry, and expiry.
- Shared tasks, ownership, dependencies, claims, and completion.
- Human escalation and resolution.
- Bounded context items.
- Backpressure and storage limits.
- Schema migrations and recovery tools.
- Loopback HTTP API and local process transport.
- CLI for daemon, status, scopes, diagnostics, and demo.

### Exit criteria

- Restart and crash tests do not lose accepted work.
- Replaced or expired executions cannot act.
- Duplicate requests cannot create duplicate work.
- Tasks cannot be claimed before their dependencies are complete.
- Every public operation has authorization and boundary tests.

## Phase 3: SDKs, MCP, and adapters

Make the Bus easy to adopt without depending on October Desktop.

### Deliverables

- TypeScript client SDK.
- Official MCP server integration.
- Streamable HTTP and stdio support where appropriate.
- Registration and lifecycle helpers.
- Pull delivery for harnesses that cannot be woken safely.
- Adapter interface for stronger native integrations.
- Two-agent and multi-agent examples.
- A reference harness adapter.
- Adapter capability profiles.

### Exit criteria

- Two independent example agents can discover, message, delegate, and reply in five minutes.
- An adapter can claim only behavior verified by tests.
- MCP and native clients enforce the same authority.

## Phase 4: Packaging and platform support

Ship Agent Intercom as normal developer infrastructure.

### Deliverables

- Signed release binaries and checksums.
- Automated builds and tests for Windows, macOS, and Linux.
- x64 and arm64 artifacts where supported.
- Clear data, configuration, log, and runtime locations.
- Service installation and removal instructions.
- Safe upgrade and rollback behavior.
- Homebrew and common Linux installation paths.
- A Windows installation path.
- Validated Omarchy service packaging.

### Exit criteria

- A clean machine can install, run the demo, restart, upgrade, and remove the Bus.
- The user does not need to install a language runtime.
- Runtime credentials are not exposed through logs, process arguments, or shared files.

## Phase 5: Direct October integration (deferred)

Use the standalone Bus as October's communication substrate after the standalone
service has launched and demonstrated production behavior. This is not a
prerequisite for the current release, and does not authorize deleting Desktop's
existing implementation or backend data.

### Deliverables

- October Desktop supervises the same public daemon shipped to everyone.
- Desktop uses the public client SDK and protocol.
- Harness-specific behavior lives behind adapter interfaces.
- Product capabilities are injected above the Bus rather than added to its core.
- Migration tooling preserves accepted messages and active task state where safe.
- Parity tests compare standalone and October-managed agent behavior.

### Exit criteria

- October does not maintain a separate message, task, or identity core.
- Standalone harnesses and October-managed harnesses pass the same conformance profile.
- Removing October Desktop does not make the local Bus state invalid.

## Phase 6: Remote and self-hosted operation

Extend the protocol without changing its local semantics.

### Deliverables

- Versioned remote endpoint contract.
- Configurable server URL and credential provider.
- Secure registration, token rotation, and revocation.
- TLS and origin validation.
- Durable remote message and task semantics.
- Reconnect, resume, retry, and duplicate suppression.
- Tenant isolation tests.
- A self-hosted reference server.
- A PostgreSQL storage implementation.
- An optional Supabase deployment profile.
- Configuration for user-owned servers and databases.

### Exit criteria

- Local and remote clients pass the same protocol suite.
- One tenant cannot discover, read, or affect another tenant.
- A network interruption cannot silently lose or duplicate accepted work.
- Clients can change endpoints without changing harness code.
- No privileged database credential is given to an agent process.

## Phase 7: Cross-machine and server-to-server coordination

Support agents running across a user's machines and servers.

### Deliverables

- Authenticated device and server enrollment interface.
- Direct or relayed transport behind the public Bus contract.
- Full message, task, lifecycle, and reply parity across machines.
- Reconnection after a laptop, server, or network restart.
- Remote harness adapters with truthful readiness evidence.
- Server-to-server interoperability.
- Tests for revocation, stale processes, competing controllers, and network partitions.

### Exit criteria

- The same agent workflow works locally, across two machines, and across two servers.
- Transport details never become agent identity or permission.
- Remote support does not expose product control-plane data through the open protocol.

## Phase 8: Managed service compatibility

Allow a hosted service without making it mandatory.

### Open-source deliverables

- Endpoint selection and configuration.
- Standard authentication hooks.
- Self-hosting documentation.
- Supabase and PostgreSQL deployment support.
- Import and export of portable Bus data where safe.
- Protocol tests that any service provider can run.

The account/workspace APIs, browser view, billing integration and deployment
source are included in this repository. Operating the hosted endpoint and
providing support are the commercial service. A managed endpoint uses the same
coordination runtime and public protocol as a self-hosted endpoint.

### Exit criteria

- Users can choose local-only, self-hosted, or managed operation.
- No harness adapter is tied to one service provider.
- Moving between compatible endpoints does not require changing agent behavior.

## Phase 9: Conformance and 1.0

Turn compatibility into a claim that can be proven.

### Deliverables

- Standalone conformance runner.
- Required local profile.
- Remote profile.
- Optional active-delivery and lifecycle profiles.
- Adapter certification fixtures.
- Security and interoperability test vectors.
- Upgrade and compatibility policy.
- Stable 1.0 protocol and reference runtime.

### Exit criteria

- A new harness developer can prove compatibility without October Desktop.
- Every compatibility claim maps to a passing profile.
- The protocol can evolve through version negotiation without breaking stable clients.

## Test matrix

The release pipeline will cover:

- Windows x64;
- macOS arm64 and x64 where supported;
- Linux x64 and arm64;
- clean install and removal;
- process crash and machine restart;
- database recovery and migrations;
- expired and replaced authority;
- queue pressure and message expiry;
- duplicate delivery and idempotency;
- local, remote, and mixed-version clients;
- hostile tenant and malformed protocol inputs.

## Current implementation

The current Go implementation provides:

- scopes and execution-bound agents;
- explicit peers and discovery;
- durable SQLite or PostgreSQL messages;
- inbox reservations and acknowledgements;
- linked requests and responses;
- shared tasks and dependencies;
- human escalation;
- loopback HTTP, hosted HTTPS and MCP tools;
- an official MCP server, TypeScript client, Go client, CLI, and two-agent demo;
- shared channels, threads, literal-text search and private workspace files;
- account membership, teammate invitations and browser-approved device pairing;
- OAuth/manual-key MCP connections and explicit recovery without losing inboxes;
- a browser workspace for sessions, conversations, tasks, decisions and usage;
- native Claude Code and experimental Codex terminal wake;
- workspace billing integration, usage limits, retention and portable export;
- isolated deployment roles and encrypted database/file backup tooling.

Protocol fixtures and conformance tests cover the runtime contract. Hosted tests
use an isolated PostgreSQL database; production/client qualification is a separate
release requirement. The TypeScript SDK remains client-only.

## Architecture decisions

The following decisions must be resolved before their phase begins:

| Decision | Status | Required result |
| --- | --- | --- |
| Core runtime | Selected | Go reference daemon |
| Client SDK | Selected | TypeScript first, more languages as needed |
| Local database | Selected | SQLite with durable restart recovery |
| Local transport | Selected | Loopback HTTP and MCP Streamable HTTP |
| Remote transport | Selected | HTTPS with HTTP/MCP, bounded waits and durable inboxes |
| Remote storage | Implemented | PostgreSQL through the same runtime operations |
| Authentication | Implemented | Local execution credentials; hosted verified accounts, device grants, OAuth and MCP keys |
| Schema format | Selected | JSON Schema 2020-12 |
| Upgrade policy | Open | Safe migrations and explicit compatibility windows |
| Adapter profiles | Open | Claims tied to conformance tests |

Resolve these decisions with small executable prototypes and tests. Avoid adding abstraction before two real implementations require it.
