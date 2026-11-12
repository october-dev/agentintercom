# A2A bridge architecture

Status: experimental

Agent Intercom is a coordination runtime and bridge. A2A is the protocol used at the boundary between compatible agents. MCP remains the primary adapter surface for coding harnesses.

## Compatibility target

The first bridge targets A2A 1.0 over HTTP+JSON. JSON-RPC and gRPC are not advertised until they are implemented and tested.

| Component | Pin |
| --- | --- |
| A2A specification | `v1.0.0` at `173695755607e884aa9acf8ce4feed90e32727a1` |
| A2A Go SDK | `v2.5.0` at `9d95b95445f4208ba77f48a137a278067937adb7` |
| A2A TCK | commit `107a5fd4ccc129b9d9335c797379779834968cd9` |

SDK versions and A2A protocol versions are independent. The bridge records both.

## Boundaries

- The Agent Intercom domain model remains canonical inside the runtime.
- A2A types are translated at transport boundaries.
- An Agent Card describes an agent. It does not grant access to the agent or its scope.
- Public cards contain no execution IDs, credentials, prompts, local paths, or private context.
- A scope owner chooses which agent cards to publish. The shared daemon does not enumerate agents publicly.
- A deployment gives each published agent a stable interface URL. Execution IDs are never public identity.
- Agent interfaces use bearer authentication. Loopback HTTP is allowed for local development. Remote interfaces require HTTPS.
- Remote principals are bound to one publication. Their credentials do not inherit scope, agent, MCP, or administrative authority.
- Agent Intercom shared work items are a coordination pool. They are not A2A Tasks. A2A Tasks represent one delegated interaction and its result stream.

The reference daemon stores owner-controlled publications, scoped remote principals, and durable A2A task correlations. Each accepted A2A message maps to one Bus request and its optional linked response. The published interface accepts A2A `SendMessage` over HTTP+JSON and returns the durable A2A Task. Text parts are supported in the first write surface. Streaming, push notifications, task listing, task reads, and cancellation are not yet supported.

Principal credentials are stored as one-way digests and shown only when created or rotated. Rotation invalidates the previous value immediately. Both the principal and its publication must be enabled for authentication to succeed.

The interface accepts a bearer credential only for the publication it was issued against. If the `A2A-Version` service parameter is present, it must contain exactly `1.0`. Unsupported message content and operations return A2A protocol errors without creating Bus work.

Each remote principal has an independent budget for unfinished messages and text bytes. The check is part of the durable acceptance transaction, so concurrent requests cannot exceed it. This prevents one caller from consuming the capacity reserved for local agents or another remote principal. Scope owners can inspect usage metadata without seeing message bodies.

### Handler caching and conditional requests

The handler returned by `a2abridge.NewAgentCardHandler` defaults to a
60-second `Cache-Control: public, max-age=60` policy with the current
time as `Last-Modified`. Use `a2abridge.NewAgentCardHandlerWithOptions`
to override either value via `HandlerOptions.CacheLifetime` and
`HandlerOptions.LastModified`. The cache lifetime must be non-negative
and at most 24 hours; the constructor rejects other values.

The handler honours both `If-None-Match` and `If-Modified-Since`. It supports
weak tags, tag lists, and wildcard cache validation. `If-None-Match` takes
precedence when both headers are present. The `ETag`, `Cache-Control`, and
`Last-Modified` headers are computed once and remain consistent for every
request served by a handler instance.

Owner-controlled publications use a zero-second cache lifetime so clients revalidate disabled cards immediately.

## Extensions

Core A2A behavior is implemented before any October-specific extension. Extension identifiers are not published until their domain and lifecycle are established. Extension support will have separate conformance evidence from core A2A support.

## Version policy

Agent Intercom is pre-1.0. Bridge APIs may change as the A2A server surface is implemented. Public compatibility claims require the applicable A2A TCK checks and released interoperability evidence.
