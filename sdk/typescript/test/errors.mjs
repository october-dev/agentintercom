import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import {
  BusError,
  OctoberBusAgentSession,
  OctoberBusClient,
  OctoberBusScopeClient,
  newIdempotencyKey,
  pollInbox,
  requiredEnvironmentValue,
  withClaimedTask
} from '../dist/index.js'

async function expectInternal(work) {
  try {
    await work()
    assert.fail('request unexpectedly succeeded')
  } catch (error) {
    assert(error instanceof BusError)
    assert.equal(error.code, 'INTERNAL')
  }
}

await expectInternal(() => new OctoberBusClient('not a url', 'token').listPeers())

assert.equal(requiredEnvironmentValue({ OCTOBER_BUS_TOKEN: 'token' }, 'OCTOBER_BUS_TOKEN'), 'token')
assert.throws(
  () => requiredEnvironmentValue({}, 'OCTOBER_BUS_TOKEN'),
  /OCTOBER_BUS_TOKEN is required/
)
assert.match(newIdempotencyKey(), /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/)

const abortController = new AbortController()
abortController.abort(new Error('stopped'))
await assert.rejects(
  () =>
    OctoberBusAgentSession.start({
      address: 'not a url',
      scopeToken: 'token',
      registration: { id: 'aborted', displayName: 'Aborted' },
      signal: abortController.signal
    }),
  /stopped/
)
await assert.rejects(
  () =>
    OctoberBusAgentSession.start({
      address: 'not a url',
      scopeToken: 'token',
      registration: { id: 'offline-ready', displayName: 'Offline Ready' },
      initialLifecycle: 'offline',
      initialReady: true
    }),
  /offline agents cannot be ready/
)

let releasedTask
const failingTaskClient = {
  async claimTask(taskId) {
    return { id: taskId, status: 'claimed' }
  },
  async releaseTask(taskId) {
    releasedTask = taskId
  }
}
await assert.rejects(
  () =>
    withClaimedTask(failingTaskClient, 'task_failure', async () => {
      throw new Error('work failed')
    }),
  /work failed/
)
assert.equal(releasedTask, 'task_failure')

let inboxPolls = 0
const pollingClient = {
  async pullInbox(limit, options) {
    assert.equal(limit, 50)
    assert.equal(options.waitMs, 25_000)
    inboxPolls += 1
    return inboxPolls === 1 ? [] : [{ id: 'message_1' }]
  }
}
const inbox = pollInbox(pollingClient)
const polled = await inbox.next()
assert.equal(polled.done, false)
assert.equal(polled.value[0].id, 'message_1')
await inbox.return()
await assert.rejects(() => pollInbox(pollingClient, { waitMs: 0 }).next(), /waitMs must be an integer between 1 and 25000/)
await assert.rejects(
  () => new OctoberBusScopeClient('not a url', 'token').watchEvents({ waitMs: 0 }).next(),
  /waitMs must be an integer between 1 and 25000/
)


// A controllable inbox client whose pullInbox blocks until a queued batch is
// delivered through it (the server wakes the blocked reserve on the ready edge)
// or its signal (terminal) aborts. Mirrors the server-side bounded wait: a ready
// host's long-poll resolves on its own rather than being aborted by a local
// wake signal.
function makeInboxClient(batches) {
  const state = {
    polls: 0,
    wakes: 0,
    queue: batches,
    blocked: 0,
    blockedResolvers: []
  }
  const client = {
    async pullInbox(limit, options) {
      state.polls += 1
      const signal = options.signal
      return new Promise((resolve) => {
        const cleanup = () => {
          signal?.removeEventListener('abort', onAbort)
          const index = state.blockedResolvers.indexOf(resolve)
          if (index >= 0) state.blockedResolvers.splice(index, 1)
        }
        const onAbort = () => {
          cleanup()
          resolve([]) // terminal abort: end this pull with an empty batch
        }
        const tryResolve = () => {
          if (state.queue.length > 0) {
            cleanup()
            resolve(state.queue.shift())
            return true
          }
          return false
        }
        if (tryResolve()) return
        state.blocked += 1
        state.blockedResolvers.push(resolve)
        signal?.addEventListener('abort', onAbort, { once: true })
      })
    },
    // Simulate the server waking the blocked reserve on a ready edge: each
    // blocked in-flight pull resolves with the next queued batch.
    wake() {
      state.wakes += 1
      const resolvers = state.blockedResolvers.splice(0)
      for (const resolve of resolvers) {
        if (state.queue.length > 0) resolve(state.queue.shift())
        else resolve([])
      }
    }
  }
  return { client, state }
}

// Ready-edge delivery: a host blocked in the long-poll reserve receives the
// queued delivery promptly when it becomes ready — the server wakes the reserve
// and delivers through the in-flight pull. No local wake abort is involved and
// the loop never terminates on its own; only the terminal signal ends it.
{
  const { client, state } = makeInboxClient([])
  const stop = new AbortController()
  const inbox = pollInbox(client, { waitMs: 25_000, signal: stop.signal })
  const first = inbox.next()
  await new Promise((resolve) => setTimeout(resolve, 30))
  assert.equal(state.blocked, 1, 'first poll blocks in the server-side wait')
  // A delivery queues while the host is blocked, then the ready edge fires and
  // the server wakes the blocked reserve, delivering the queued batch.
  state.queue.push([{ id: 'queued_before_ready' }])
  client.wake()
  const result = await first
  assert.equal(result.done, false)
  assert.equal(result.value[0].id, 'queued_before_ready')
  // The loop continues after the delivery (it did not terminate).
  stop.abort()
  await inbox.return()
}

// Concurrent polling: two independent pollInbox loops, each blocked in its own
// server-side wait, deliver independently without cross-talk or termination.
{
  const { client: clientA, state: stateA } = makeInboxClient([])
  const { client: clientB, state: stateB } = makeInboxClient([])
  const stopA = new AbortController()
  const stopB = new AbortController()
  const loopA = pollInbox(clientA, { waitMs: 25_000, signal: stopA.signal })
  const loopB = pollInbox(clientB, { waitMs: 25_000, signal: stopB.signal })
  const nextA = loopA.next()
  const nextB = loopB.next()
  await new Promise((resolve) => setTimeout(resolve, 30))
  assert.equal(stateA.blocked, 1)
  assert.equal(stateB.blocked, 1)
  stateA.queue.push([{ id: 'a_1' }])
  clientA.wake()
  assert.deepEqual((await nextA).value, [{ id: 'a_1' }])
  assert.equal(stateB.blocked, 1, 'loop B was not woken by loop A')
  stateB.queue.push([{ id: 'b_1' }])
  clientB.wake()
  assert.deepEqual((await nextB).value, [{ id: 'b_1' }])
  stopA.abort()
  stopB.abort()
  await loopA.return()
  await loopB.return()
}

// Response/terminal race: pullInbox returns a committed non-empty batch while
// the terminal signal also aborts. The committed batch MUST be yielded exactly
// once before the loop honors the abort, so a response racing an abort can
// never drop already-committed mail.
{
  const state = { polls: 0 }
  const stop = new AbortController()
  const client = {
    async pullInbox(_limit, options) {
      state.polls += 1
      // Abort the terminal signal in the same tick that the committed batch
      // returns, simulating a race between a response and a terminal abort.
      // Use a real AbortController so AbortSignal.aborted is true.
      stop.abort()
      return [{ id: 'committed_in_race' }]
    }
  }
  const inbox = pollInbox(client, { waitMs: 25_000, signal: stop.signal })
  const first = await inbox.next()
  assert.equal(first.done, false)
  assert.equal(first.value.length, 1)
  assert.equal(first.value[0].id, 'committed_in_race', 'committed batch must be delivered exactly once')
  // The loop must now honor the terminal abort and stop (not re-poll).
  const second = await inbox.next()
  assert.equal(second.done, true, 'loop must end after committed batch plus terminal abort')
  assert.equal(state.polls, 1, 'loop must not re-poll after a committed batch plus terminal abort')
}

// Listener hygiene + terminal semantics: pullInbox is cancelled only by the
// terminal signal, and a terminal abort always ends the loop.
{
  const { client, state } = makeInboxClient([])
  const stop = new AbortController()
  const inbox = pollInbox(client, { waitMs: 25_000, signal: stop.signal })
  const pending = inbox.next()
  await new Promise((resolve) => setTimeout(resolve, 20))
  assert.equal(state.blocked, 1)
  // The terminal abort ends the in-flight pull (empty batch) and the loop.
  stop.abort()
  const ended = await inbox.next()
  assert.equal(ended.done, true, 'terminal abort must end the generator')
  await inbox.return()
}
const server = createServer((_request, response) => {
  response.writeHead(502, { 'content-type': 'text/plain' })
  response.end('upstream unavailable')
})
server.listen(0, '127.0.0.1')
await once(server, 'listening')

try {
  const address = server.address()
  assert(address && typeof address === 'object')
  await expectInternal(() =>
    new OctoberBusClient(`http://127.0.0.1:${address.port}`, 'token').listPeers()
  )
} finally {
  server.close()
  await once(server, 'close')
}

const hangingServer = createServer(() => {})
hangingServer.listen(0, '127.0.0.1')
await once(hangingServer, 'listening')

try {
  const address = hangingServer.address()
  assert(address && typeof address === 'object')
  const client = new OctoberBusClient(`http://127.0.0.1:${address.port}`, 'token')
  await assert.rejects(() => client.listPeers({ timeoutMs: 10 }), /timed out after 10ms/)
  const cancelled = new AbortController()
  cancelled.abort(new Error('cancelled by caller'))
  await assert.rejects(() => client.listPeers({ signal: cancelled.signal }), /cancelled by caller/)
  await assert.rejects(() => client.listPeers({ timeoutMs: 0 }), (error) => {
    assert(error instanceof BusError)
    assert.equal(error.code, 'INVALID_ARGUMENT')
    return true
  })
} finally {
  hangingServer.closeAllConnections()
  hangingServer.close()
  await once(hangingServer, 'close')
}
