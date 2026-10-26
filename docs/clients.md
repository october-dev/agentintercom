# Client SDKs

Agent Intercom provides a Go client in this module and the `agentintercom-cli` npm package for the TypeScript client and matching native Go daemon. Its terminal command is `agentintercom`. SDK imports do not load or start the daemon. Historical npm versions below refer to the previous `@october-dev/october-bus` package.

## Runtime and SDK compatibility

| Client / distribution | Runtime requirement | Boundary |
| --- | --- | --- |
| Go module pinned to `v0.1.0-rc.4` | rc.4's documented protocol subset | Does not include the new managed-retirement helper. |
| npm versions before `next.15` | Check that published version's documentation | SDK-only packages; installing one does not install a daemon. |
| Updated Go/TypeScript managed sessions in this branch | Ready protocol `0.1` with `/health.features` containing `session-retirement` | Checked before registration; rc.4 and missing feature declarations are rejected. |
| Bundled npm CLI, starting with published `next.15` | Its exact-version native optional package | Launcher rejects missing/mismatched native packages; remote SDK connections are still checked separately. |
| Direct HTTP/SDK calls | An endpoint implementing each operation used | Low-level clients do not negotiate every optional operation automatically. |

The health feature declaration is additive and independent of version labels. It does not confer authority: registration, heartbeat and retirement still authenticate every request. Missing `features` means no declared features; ignore unknown feature names. A compatible independently implemented daemon can advertise `session-retirement` only if it implements the complete retirement contract. Managed-session startup returns `CONFLICT` for incompatible health; transport or unavailable-health errors retain their normal failure semantics. No scope or agent token is sent to the health endpoint.

Before stable, pin the versions you deploy together. Breaking protocol changes require a new protocol version and retained prior specification, even while runtime/SDK packages are prereleases. Do not infer availability from a source manifest or infer feature support from a higher-looking version number.

## Credentials

Use the narrowest credential for each operation:

- admin token for scope creation, portable import and export, and daemon shutdown;
- scope token for agent registration, peer links, Agent Card publications and remote principals, project task management, event streams, storage controls, and human escalation resolution;
- agent token for heartbeat, discovery, messages, tasks, and escalation creation;
- scoped A2A credential for one published A2A interface only;
- scoped output credential for read or publish access to one output stream.

Keep admin and scope tokens outside model context. A managed session gives the harness only its execution-bound agent token.

## Go

```go
ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
defer cancel()

owner := bus.Client{Address: address, Token: scopeToken}
registration, err := owner.RegisterAgent(ctx, bus.RegisterAgentInput{
    ID:          "reviewer",
    DisplayName: "Reviewer",
    ConnectTo:   []string{"planner"},
})
if err != nil {
    return err
}

agent := bus.Client{Address: address, Token: registration.AgentToken}
peers, err := agent.ListPeers(ctx)
messages, err := agent.PullInbox(ctx, 50, 25*time.Second)

ownerTasks, err := owner.ListTasks(ctx, true)
storage, err := owner.StorageSummary(ctx)
events, err := owner.Events(ctx, lastRevision, 50, 25*time.Second)
publication, err := owner.CreateAgentCardPublication(ctx, bus.PublishAgentCardInput{AgentID: "reviewer"})
issued, err := owner.CreateA2APrincipal(ctx, bus.CreateA2APrincipalInput{
    PublicationID: publication.ID,
    Label:         "CI reviewer",
})
// Store issued.Credential securely. It cannot be retrieved later.

// Use an A2A 1.0 HTTP+JSON client against publication.InterfaceURL.
// Send the issued credential as a bearer token. The first write surface
// accepts text-only SendMessage requests and returns a durable A2A Task.

principalUsage, err := owner.ListA2APrincipalUsage(ctx)

for batch, err := range owner.WatchEvents(ctx, lastRevision, 50) {
    if err != nil {
        return err
    }
    if batch.ResyncRequired {
        break
    }
    lastRevision = batch.NextRevision
}

progress, err := agent.AddTaskProgress(ctx, taskID, bus.AddTaskProgressInput{
    Kind: "progress",
    Text: "Retry behavior is implemented.",
})

stream, err := owner.CreateOutputStream(ctx, bus.CreateOutputStreamInput{
    Name:              "site-preview",
    PublisherAgentIDs: []string{"reviewer"},
})
value, err := agent.PublishOutput(ctx, stream.ID, bus.PublishOutputInput{
    ContentType: bus.OutputJSON,
    Value:       map[string]any{"status": "ready", "url": "https://example.test/preview"},
})
reader, err := owner.CreateOutputPrincipal(ctx, bus.CreateOutputPrincipalInput{
    StreamID:    stream.ID,
    Label:       "Preview page",
    Permissions: []bus.OutputPermission{bus.OutputRead},
})
latest, err := (bus.Client{Address: address, Token: reader.Credential}).LatestOutput(ctx, stream.ID)
```

Every Go call accepts a context. The default HTTP client has a 30-second timeout. Supply `Client.HTTP` to set a different transport or timeout.

Use `bus.StartAgentSession` when an adapter needs registration, heartbeat, execution-replacement detection, and retirement managed outside the model loop. Close and context cancellation attempt to retire the execution and release claims/reservations; `Done` closes after cleanup is attempted. Inspect `Err` for heartbeat or cleanup failure. A replaced token can never retire its successor. This helper requires the retirement endpoint and is not backward compatible with the rc.4 daemon.

Go `SetState` and TypeScript `setState` serialize writes and update their remembered state only after success. Scheduled heartbeats read the last confirmed state in that same ordering. A failed network response is not proof of rollback on the server: retry the desired state or close the session instead of assuming the failed change took effect.

## TypeScript

Version `0.2.0` is published under `candidate`. Install it explicitly; the
`latest` tag still selects the links-only `agentintercom-cli@0.0.1` placeholder:

```bash
npm install agentintercom-cli@0.2.0
```

```ts
import { AgentIntercomAgentSession, AgentIntercomOutputClient, AgentIntercomScopeClient } from 'agentintercom-cli'

const session = await AgentIntercomAgentSession.start({
  address,
  scopeToken,
  registration: {
    id: 'reviewer',
    displayName: 'Reviewer',
    connectTo: ['planner']
  }
})

await session.setState('ready', true)
const peers = await session.client.listPeers({ timeoutMs: 10_000 })
const messages = await session.client.pullInbox(50, { waitMs: 25_000 })
const readyTasks = await new AgentIntercomScopeClient(address, scopeToken).listTasks({ ready: true })
const owner = new AgentIntercomScopeClient(address, scopeToken)
const publication = await owner.createAgentCardPublication({ agentId: 'reviewer' })
const issued = await owner.createA2APrincipal({
  publicationId: publication.id,
  label: 'CI reviewer'
})
// Store issued.credential securely. It cannot be retrieved later.

for await (const batch of owner.watchEvents({ after: lastRevision })) {
  if (batch.resyncRequired) break
  lastRevision = batch.nextRevision
}

await session.client.addTaskProgress(taskId, {
  kind: 'progress',
  text: 'Retry behavior is implemented.'
})

const stream = await owner.createOutputStream({
  name: 'site-preview',
  publisherAgentIds: ['reviewer']
})
await session.client.publishOutput(stream.id, {
  contentType: 'application/json',
  value: { status: 'ready', url: 'https://example.test/preview' }
})
const reader = await owner.createOutputPrincipal({
  streamId: stream.id,
  label: 'Preview page',
  permissions: ['read']
})
const outputs = new AgentIntercomOutputClient(address, reader.credential)
const latest = await outputs.latest(stream.id)
```

Each TypeScript operation accepts an optional final `{ timeoutMs, signal }` argument. Inbox and event operations support bounded waits up to 25 seconds. The default request timeout is 30 seconds.

Persist an event batch's `nextRevision` only after applying the whole batch. If `resyncRequired` is true, rebuild from the resource APIs before saving the returned cursor. Event envelopes contain state metadata but not message, task, progress, or escalation contents.

Store a remote principal credential when it is created. It cannot be retrieved later. Rotation returns a replacement and invalidates the previous value immediately. Principal lists never include credentials.

Output history is independent of the scope event stream. Use `nextSequence` to continue an ordered read. If `resyncRequired` is true, read the latest value and resume from its sequence. Scope events include output metadata but never the published value or reference.

The [live output page](../examples/output-stream) shows how a browser can display a coding agent's latest value with a read-only credential.

Prefer bounded inbox waiting for efficient pull delivery. `pollInbox` provides an async iterator over repeated bounded waits. Use `withClaimedTask` to release a task if work or completion fails. Keep the managed session alive while holding a claim.

## Errors

Go returns `*bus.BusError`. TypeScript throws `BusError`. Branch on the protocol error code instead of matching the human-readable message.

```ts
import { BusError } from 'agentintercom-cli'

try {
  await session.client.claimTask(taskId)
} catch (error) {
  if (error instanceof BusError && error.code === 'CONFLICT') {
    // The task is blocked, done, or claimed by another execution.
  }
}
```

The public error codes and HTTP mappings are defined in the [protocol specification](../spec/0.1/README.md).
