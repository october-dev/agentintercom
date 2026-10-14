import assert from 'node:assert/strict'
import test from 'node:test'
import { OctoberBusAgentSession } from '../dist/index.js'

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
  await assert.rejects(OctoberBusAgentSession.start(options), /initial heartbeat failed/)
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
  const session = await OctoberBusAgentSession.start(options)
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
  const starting = OctoberBusAgentSession.start({ ...options, signal: controller.signal })
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
  const session = await OctoberBusAgentSession.start({ ...options, heartbeatIntervalMs: 5 })
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
  const session = await OctoberBusAgentSession.start({ ...options, heartbeatIntervalMs: 5 })
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
    await assert.rejects(OctoberBusAgentSession.start(options), error => error.code === 'CONFLICT')
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
  const session = await OctoberBusAgentSession.start(options)
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

test('done stays pending until retirement settles and close reports no spurious error', async (t) => {
  // Harsh's shutdown race: hold an explicit setState response; let the
  // background timer enqueue behind it; call close(); release the explicit
  // response; hold the offline retire response. At that point session.done
  // must still be pending, close() must still be pending, and session.error
  // must be undefined — a deliberate shutdown is not a session failure.
  const explicitStarted = deferred()
  const releaseExplicit = deferred()
  const retireStarted = deferred()
  const releaseRetire = deferred()
  let beats = 0
  mockRuntime(t, async (url, request) => {
    if (url.endsWith('/agents')) return ok(registration)
    if (url.endsWith('/retire')) {
      retireStarted.resolve()
      await releaseRetire.promise
      return ok({ retired: true })
    }
    if (++beats === 1) return ok({ lifecycle: 'starting' }) // initial heartbeat
    // First explicit setState: hold it so the background beat queues behind it.
    explicitStarted.resolve()
    await releaseExplicit.promise
    return ok(JSON.parse(request.body))
  })
  const session = await OctoberBusAgentSession.start({ ...options, heartbeatIntervalMs: 5 })
  t.after(() => session.close())
  const explicit = session.setState('working', true)
  await explicitStarted.promise
  // Give the 5ms background timer room to fire and enqueue behind the explicit write.
  await new Promise((resolve) => setTimeout(resolve, 25))
  const closing = session.close()
  releaseExplicit.resolve()
  await explicit
  // The queued background heartbeat now rejects with 'agent session is closed'
  // (expected cancellation), and close proceeds to the held retire.
  await retireStarted.promise
  let doneSettled = false
  void session.done.then(() => { doneSettled = true })
  await new Promise((resolve) => setTimeout(resolve, 20))
  assert.equal(doneSettled, false, 'done must stay pending while offline cleanup is held')
  assert.equal(session.error, undefined, 'intentional close must not produce a spurious session error')
  releaseRetire.resolve()
  await Promise.all([closing, session.done])
  assert.equal(doneSettled, true)
  assert.equal(session.error, undefined)
})

test('wake re-subscription inside the abort callback observes every ready edge', async (t) => {
  // Harsh's re-arming repro: a host that re-arms its listener inside the
  // readiness callback must be notified on every false→true transition.
  // The replacement controller is installed before the previous one aborts,
  // so a callback reading session.wake mid-dispatch lands on a live signal.
  mockRuntime(t, async (url, request) => {
    if (url.endsWith('/agents')) return ok(registration)
    if (url.endsWith('/retire')) return ok({ retired: true })
    return ok(JSON.parse(request.body))
  })
  const session = await OctoberBusAgentSession.start(options)
  t.after(() => session.close())
  let observed = 0
  const rearm = () => {
    session.wake.addEventListener('abort', () => {
      observed++
      rearm()
    }, { once: true })
  }
  rearm()
  await session.setState('ready', true)
  await session.setState('working', false)
  await session.setState('ready', true)
  assert.equal(observed, 2, 'every false→true transition must notify, including re-armed listeners')
})
