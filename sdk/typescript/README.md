# October Bus CLI and TypeScript Client

Typed clients and protocol definitions for connecting Node.js applications and harness adapters to October Bus.

Starting with `0.1.0-next.15`, this package includes a launcher for the native Go daemon and the TypeScript client. npm installs the matching binary through an exact-version optional dependency; it does not compile Go or run a download script on your machine. Existing TypeScript imports are unchanged. Older npm releases contain only the client.

The client is in active development and has not reached a stable release. Before 1.0, its API, schemas, and protocol behavior may change between releases.

Managed sessions check unauthenticated `/health` before registration and require a ready protocol `0.1` runtime advertising `features: ["session-retirement"]`. A missing feature or incompatible protocol fails with `BusError.code === 'CONFLICT'` before replacing an execution. Close and cancellation drain lifecycle writes and attempt permanent execution retirement; temporary offline heartbeats are distinct. Inspect `session.error` for heartbeat or cleanup failure. Failed cleanup falls back to lease expiry. Do not use this updated session helper against rc.4.

`setState` commits the local state only after a successful heartbeat. Background heartbeats use the last confirmed state. A network failure can still have an ambiguous server outcome: callers must retry the desired state or close the session; the helper does not promise that a failed response means the server made no change.

Admins can use `listScopes()`, `rotateScopeToken(scopeId)`, and `deleteScope(scopeId)` for recovery. Rotation also retires executions and disables scoped credentials; deletion is permanent. Both agent and scope clients expose `taskPage(after?, limit?, options?)` for bounded history traversal.

`for await (const chunk of admin.backup())` streams a full SQLite snapshot without accumulating it in memory. Write it to a new private file, discard partial output if iteration fails, and retain the original database when restoring. This snapshot includes credentials and is not a portable scope archive.

`OctoberBusAdminClient` can export and import versioned portable scope archives. Archives preserve durable collaboration state but exclude credentials, leases, and active execution authority.

## Install

Install the current prerelease from the `next` tag:

```sh
npm install @october-dev/october-bus@next
```

The native CLI is available in `0.1.0-next.15`. Earlier published versions are SDK-only, and unqualified `npx` selects `latest`, not `next`; use the explicit version below or `next`.

Run the CLI without installing Go:

```sh
npx @october-dev/october-bus@0.1.0-next.15 demo
npx @october-dev/october-bus@0.1.0-next.15 start
```

macOS, Linux, and Windows binaries are provided for x64 and arm64. Linux binaries are built without CGO. Keep optional dependencies enabled; `--ignore-scripts` is supported. If you installed with `--omit=optional`, reinstall with `--include=optional` to use the CLI. SDK-only consumers can omit the binary packages and keep using a separately installed daemon. Do not copy `node_modules` between platforms.

The CLI refuses mismatched binary versions and never falls back to another executable on `PATH`. Native archives remain available from [GitHub releases](https://github.com/october-dev/october-bus/releases).

## Example

Start the daemon, create a scope with the October Bus CLI, and set the returned token as `OCTOBER_BUS_SCOPE_TOKEN`.

```ts
import { OctoberBusClient, OctoberBusOutputClient, OctoberBusScopeClient } from '@october-dev/october-bus'

const address = 'http://127.0.0.1:4765'
const scope = new OctoberBusScopeClient(address, process.env.OCTOBER_BUS_SCOPE_TOKEN!)

const plannerRegistration = await scope.registerAgent({
  id: 'planner',
  displayName: 'Planner'
})
const reviewerRegistration = await scope.registerAgent({
  id: 'reviewer',
  displayName: 'Reviewer',
  connectTo: ['planner']
})

const planner = new OctoberBusClient(address, plannerRegistration.agentToken)
const reviewer = new OctoberBusClient(address, reviewerRegistration.agentToken)

const task = await scope.addTask({
  title: 'Review checkout retries',
  description: 'Check idempotency and error handling.'
})
const readyTasks = await scope.listTasks({ ready: true })
const storage = await scope.storageSummary()

const publication = await scope.createAgentCardPublication({ agentId: 'reviewer' })
const issued = await scope.createA2APrincipal({
  publicationId: publication.id,
  label: 'CI reviewer'
})
// Store issued.credential securely. It cannot be retrieved later.
const principalUsage = await scope.listA2APrincipalUsage()

const outputStream = await scope.createOutputStream({
  name: 'site-preview',
  publisherAgentIds: ['reviewer']
})
await reviewer.publishOutput(outputStream.id, {
  contentType: 'application/json',
  value: { status: 'ready', url: 'https://example.test/preview' }
})
const outputReader = await scope.createOutputPrincipal({
  streamId: outputStream.id,
  label: 'Preview page',
  permissions: ['read']
})
const outputs = new OctoberBusOutputClient(address, outputReader.credential)
const latestOutput = await outputs.latest(outputStream.id)

const dryRun = await scope.pruneScope({ before: '2026-08-01T00:00:00Z' })

const claimed = await reviewer.claimTask(task.id)
await reviewer.addTaskProgress(claimed.id, {
  kind: 'progress',
  text: 'Checked idempotency. Reviewing error handling now.'
})

const receipt = await planner.sendMessage({
  to: 'reviewer',
  mode: 'request',
  body: 'Review the retry path',
  idempotencyKey: crypto.randomUUID()
})

const messages = await reviewer.pullInbox()
await reviewer.acknowledgeMessages(messages.map((message) => message.id))
console.log(receipt.messageId, messages)
```

An idle agent can wait for new work without a polling loop:

```ts
const messages = await reviewer.pullInbox(50, { waitMs: 25_000 })
```

The server caps each wait at 25 seconds. Cancellation through `AbortSignal` does not reserve or lose a message.

Scope credentials create agents, manage the project task board, and handle human escalations. Agent credentials discover peers, exchange messages, coordinate tasks, and ask for human input. Claims and completion always require an execution-bound agent credential.

A remote principal credential is returned only when the principal is created or rotated. Store it securely. It is restricted to one published A2A interface and cannot access the Bus API or MCP endpoint.

Operations time out after 30 seconds by default. Pass `{ timeoutMs, signal }` as the final method argument to set a shorter deadline or cancel a request.

Generate a new idempotency key for each logical send. Keys remain bound to their original message. Keep heartbeats running while an execution holds a task claim, or the claim may be released for another agent.

`OctoberBusAgentSession` manages registration, conservative lifecycle state, heartbeat, execution replacement, and shutdown cleanup for adapters that use the TypeScript client. It exposes a `wake` signal that fires on every false→true ready transition, so a host can observe its own readiness edge.

`pollInbox` provides an abortable async iterator over repeated bounded inbox waits. The server wakes a blocked `ReserveInbox` on the ready edge, so a host reporting ready promptly receives queued deliveries through its in-flight long-poll — no client-side wake wiring is needed. Every returned batch belongs to the host; `pollInbox` never pulls and discards. `withClaimedTask` releases a claim when work or completion fails. Use both with a live agent session so the execution lease remains current while work is claimed.
