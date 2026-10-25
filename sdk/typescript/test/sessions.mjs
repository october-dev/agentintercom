import assert from 'node:assert/strict'
import test from 'node:test'
import { AgentIntercomAgentSession } from '../dist/index.js'

const options = { address: 'http://session.invalid', scopeToken: 'synthetic', registration: { id: 'worker', displayName: 'Worker' } }
const ok = (result) => new Response(JSON.stringify({ ok: true, result }), { status: 200 })
const registration = { scopeId: 'scope', agentId: 'worker', executionId: 'exec_test', agentToken: 'synthetic', leaseExpiresAt: new Date(Date.now() + 300000).toISOString() }
const deferred = () => {
  let resolve
  const promise = new Promise((done) => { resolve = done })
  return { promise, resolve }
}
const healthy = { name: 'october-bus', protocolVersion: '0.1', status: 'ready', features: ['session-retirement'] }
const mockRuntime = (t, handler) => t.mock.method(globalThis, 'fetch', async (url, request) =>
  url.endsWith('/health') ? ok(healthy) : handler(url, request))

test('failed initial heartbeat retires registration without hiding the startup error', async (t) => {
  let retired = 0
  mockRuntime(t, async (url) => {
    if (url.endsWith('/agents')) return ok(registration)
    if (url.endsWith('/retire')) { retired++; return ok({ retired: true }) }
    throw new Error('initial heartbeat failed')
  })
  await assert.rejects(AgentIntercomAgentSession.start(options), /initial heartbeat failed/)
  assert.equal(retired, 1)
})

test('close drains setState, is shared, and rejects further operations', async (t) => {
  const workingStarted = deferred()
  const releaseWorking = deferred()
  const order = []
  mockRuntime(t, async (url, request) => {
    if (url.endsWith('/agents')) return ok(registration)
    if (url.endsWith('/retire')) { order.push('retired'); return ok({ retired: true }) }
    const { lifecycle } = JSON.parse(request.body)
    if (lifecycle === 'working') { workingStarted.resolve(); await releaseWorking.promise }
    order.push(lifecycle)
    return ok({ lifecycle })
  })
  const session = await AgentIntercomAgentSession.start(options)
  t.after(() => session.close())
  const updating = session.setState('working', true)
  await workingStarted.promise
  const closing = session.close()
  assert.equal(session.close(), closing)
  await assert.rejects(session.setState('ready', true), /closed/)
  releaseWorking.resolve()
  await Promise.all([updating, closing, session.done])
  assert.deepEqual(order, ['starting', 'working', 'retired'])
})

test('abort during registration retires once the committed result arrives', async (t) => {
  const controller = new AbortController()
  const registered = deferred()
  const registering = deferred()
  let retired = 0
  mockRuntime(t, async (url) => {
    if (url.endsWith('/agents')) { registering.resolve(); await registered.promise; return ok(registration) }
    if (url.endsWith('/retire')) { retired++; return ok({ retired: true }) }
    assert.fail('canceled startup must not heartbeat')
  })
  const starting = AgentIntercomAgentSession.start({ ...options, signal: controller.signal })
  await registering.promise
  controller.abort(new Error('canceled startup'))
  registered.resolve()
  await assert.rejects(starting, /canceled startup/)
  assert.equal(retired, 1)
})

test('heartbeat failure still attempts retirement and preserves the error', async (t) => {
  let beats = 0
  let retired = 0
  mockRuntime(t, async (url) => {
    if (url.endsWith('/agents')) return ok(registration)
    if (url.endsWith('/retire')) { retired++; throw new Error('cleanup failed') }
    if (++beats > 1) throw new Error('authority lost')
    return ok({ lifecycle: 'starting' })
  })
  const session = await AgentIntercomAgentSession.start({ ...options, heartbeatIntervalMs: 5 })
  t.after(() => session.close())
  await session.done
  assert.match(String(session.error), /authority lost/)
  assert.equal(retired, 1)
  await session.close()
  assert.equal(retired, 1)
})

test('close cancels an in-flight background heartbeat without reporting failure', async (t) => {
  const heartbeatStarted = deferred()
  let beats = 0
  let retired = 0
  mockRuntime(t, async (url, request) => {
    if (url.endsWith('/agents')) return ok(registration)
    if (url.endsWith('/retire')) { retired++; return ok({ retired: true }) }
    if (++beats === 1) return ok({ lifecycle: 'starting' })
    heartbeatStarted.resolve()
    return new Promise((resolve, reject) => {
      request.signal.addEventListener('abort', () => reject(request.signal.reason), { once: true })
    })
  })
  const session = await AgentIntercomAgentSession.start({ ...options, heartbeatIntervalMs: 5 })
  t.after(() => session.close())
  await heartbeatStarted.promise
  await session.close()
  assert.equal(session.error, undefined)
  assert.equal(retired, 1)
})

test('incompatible health fails before sending credentials or replacing an execution', async (t) => {
  for (const health of [{ ...healthy, features: undefined }, { ...healthy, protocolVersion: '0.2' }, { ...healthy, status: 'not_ready' }]) {
    let calls = 0
    const mock = t.mock.method(globalThis, 'fetch', async (url, request) => {
      calls++
      assert.ok(url.endsWith('/health'))
      assert.equal(request.headers.authorization, undefined)
      return ok(health)
    })
    await assert.rejects(AgentIntercomAgentSession.start(options), error => error.code === 'CONFLICT')
    assert.equal(calls, 1)
    mock.mock.restore()
  }
})

test('failed state writes do not become background state; queued writes remain ordered', async (t) => {
  const started = deferred()
  const release = deferred()
  const calls = []
  mockRuntime(t, async (url, request) => {
    if (url.endsWith('/agents')) return ok(registration)
    if (url.endsWith('/retire')) return ok({ retired: true })
    const state = JSON.parse(request.body)
    calls.push(state.lifecycle)
    if (state.lifecycle === 'ready') { started.resolve(); await release.promise }
    if (state.lifecycle === 'working') throw new Error('state rejected')
    return ok(state)
  })
  const session = await AgentIntercomAgentSession.start(options)
  t.after(() => session.close())
  const ready = session.setState('ready', true)
  await started.promise
  const failed = assert.rejects(session.setState('working', false), /state rejected/)
  // Invoke the same queue operation used by the timer without real-time waits.
  const background = session.enqueueHeartbeat()
  assert.deepEqual(calls, ['starting', 'ready'])
  release.resolve()
  await Promise.all([ready, failed, background])
  assert.deepEqual(calls, ['starting', 'ready', 'working', 'ready'])
})
