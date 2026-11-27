import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import {
  BusAbortError,
  BusError,
  BusNetworkError,
  BusTimeoutError,
  BusTransportError,
  AgentIntercomAgentSession,
  AgentIntercomClient,
  AgentIntercomScopeClient,
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

await expectInternal(() => new AgentIntercomClient('not a url', 'token').listPeers())

{
  // Nothing listens on this port once its server is closed.
  const closed = createServer()
  closed.listen(0, '127.0.0.1')
  await once(closed, 'listening')
  const { port } = closed.address()
  closed.close()
  await once(closed, 'close')
  await assert.rejects(() => new AgentIntercomClient(`http://127.0.0.1:${port}`, 'token').listPeers(), (error) => {
    assert(error instanceof BusNetworkError && error instanceof BusTransportError)
    assert.equal(error.transport, 'network')
    assert.equal(error.code, 'INTERNAL')
    assert.equal(error.status, 500)
    assert(error.cause !== undefined)
    return true
  })
}

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
    AgentIntercomAgentSession.start({
      address: 'not a url',
      scopeToken: 'token',
      registration: { id: 'aborted', displayName: 'Aborted' },
      signal: abortController.signal
    }),
  /stopped/
)
await assert.rejects(
  () =>
    AgentIntercomAgentSession.start({
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
  () => new AgentIntercomScopeClient('not a url', 'token').watchEvents({ waitMs: 0 }).next(),
  /waitMs must be an integer between 1 and 25000/
)

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
    new AgentIntercomClient(`http://127.0.0.1:${address.port}`, 'token').listPeers()
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
  const client = new AgentIntercomClient(`http://127.0.0.1:${address.port}`, 'token')
  await assert.rejects(() => client.listPeers({ timeoutMs: 10 }), (error) => {
    assert(error instanceof BusTimeoutError && error instanceof BusTransportError && error instanceof BusError)
    assert.match(error.message, /timed out after 10ms/)
    assert.equal(error.transport, 'timeout')
    assert.equal(error.timeoutMs, 10)
    assert.equal(error.code, 'INTERNAL')
    assert.equal(error.name, 'BusTimeoutError')
    return true
  })
  const cancelled = new AbortController()
  const reason = new Error('cancelled by caller')
  cancelled.abort(reason)
  await assert.rejects(() => client.listPeers({ signal: cancelled.signal }), (error) => {
    assert(error instanceof BusAbortError && error instanceof BusError)
    assert.match(error.message, /cancelled by caller/)
    assert.equal(error.transport, 'aborted')
    assert.equal(error.cause, reason)
    return true
  })
  // Aborting while the request is in flight is also reported as an abort.
  const inFlight = new AbortController()
  setTimeout(() => inFlight.abort(new Error('stopped mid-request')), 20)
  await assert.rejects(() => client.listPeers({ signal: inFlight.signal, timeoutMs: 5_000 }), (error) => {
    assert(error instanceof BusAbortError)
    assert.match(error.message, /stopped mid-request/)
    return true
  })
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
