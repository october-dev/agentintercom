import assert from 'node:assert/strict'
import { execFileSync, spawn } from 'node:child_process'
import { createHash } from 'node:crypto'
import { chmodSync, copyFileSync, mkdirSync, mkdtempSync, readFileSync, readdirSync, realpathSync, rmSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { setTimeout as delay } from 'node:timers/promises'
import { fileURLToPath } from 'node:url'

export function validateRunFile(run, pid) {
  const url = new URL(run.address)
  assert.ok(url.protocol === 'http:' && url.hostname === '127.0.0.1' && url.port &&
    !url.username && !url.password && url.pathname === '/' && !url.search && !url.hash, 'Rehearsal must use its own loopback daemon')
  assert.equal(run.pid, pid, 'Run file must belong to the child started by this rehearsal')
  assert.equal(typeof run.adminToken, 'string')
  assert.equal(run.adminToken.length, 43)
  return run
}

async function rehearse() {
  assert.ok(process.env.OCTOBER_BUS_RC4_BINARY && process.env.OCTOBER_BUS_BINARY, 'Supply checksum-verified OCTOBER_BUS_RC4_BINARY and OCTOBER_BUS_BINARY; this script never downloads binaries')
  const old = realpathSync(process.env.OCTOBER_BUS_RC4_BINARY)
  const candidate = realpathSync(process.env.OCTOBER_BUS_BINARY)
  assert.notEqual(old, candidate, 'Use distinct rc.4 and candidate executables')
  const version = binary => execFileSync(binary, ['version'], { encoding: 'utf8', timeout: 5000 }).trim()
  assert.match(version(old), /^agentintercom v?0\.1\.0-rc\.4 \(protocol 0\.1\)$/)
  const candidateVersion = version(candidate)
  assert.ok(!/\brc\.4\b|\bdev\b/.test(candidateVersion), 'Candidate must embed its release version')
  const root = mkdtempSync(join(tmpdir(), 'october-upgrade-'))
  chmodSync(root, 0o700)
  const children = []
  let passed = false
  // Never inherit a real Bus token, address, database path, or runtime setting.
  const baseEnv = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.toUpperCase().startsWith('OCTOBER_BUS_')))
  const env = (data, runtime) => ({ ...baseEnv, OCTOBER_BUS_DATA_DIR: data, OCTOBER_BUS_RUNTIME_DIR: runtime })
  const api = async (run, token, method, path, body) => {
    const response = await fetch(run.address + path, {
      method, redirect: 'error', signal: AbortSignal.timeout(10_000),
      headers: { ...(token ? { authorization: `Bearer ${token}` } : {}), ...(body === undefined ? {} : { 'content-type': 'application/json' }) },
      ...(body === undefined ? {} : { body: JSON.stringify(body) })
    })
    const result = await response.json()
    assert.ok(response.ok && result.ok !== false, `${method} ${path} returned HTTP ${response.status}`)
    return result.ok === true ? result.result : result
  }
  const start = async (binary, data, label) => {
    const runtime = join(root, label)
    const child = spawn(binary, ['start'], { env: env(data, runtime), stdio: 'ignore', shell: false })
    const state = { child, closed: false, code: undefined }
    state.finished = new Promise(resolve => {
      child.once('error', () => { state.closed = true; resolve() })
      child.once('close', code => { state.closed = true; state.code = code; resolve() })
    })
    children.push(state)
    for (let attempts = 0; attempts < 200; attempts++) {
      assert.ok(!state.closed, `${label}: daemon exited before readiness`)
      let run
      try { run = JSON.parse(readFileSync(join(runtime, 'bus.json'), 'utf8')) } catch (error) {
        if (error.code !== 'ENOENT' && !(error instanceof SyntaxError)) throw error
      }
      if (run) {
        state.run = validateRunFile(run, child.pid)
        assert.equal((await api(run, '', 'GET', '/health')).status, 'ready')
        return state
      }
      await delay(50)
    }
    throw new Error(`${label}: readiness timed out`)
  }
  const waitClosed = async state => {
    for (let attempts = 0; attempts < 200 && !state.closed; attempts++) await delay(50)
    assert.ok(state.closed, 'Child did not exit within 10 seconds')
  }
  const stop = async state => {
    await api(state.run, state.run.adminToken, 'POST', '/v1/admin/shutdown', {})
    await waitClosed(state)
    assert.equal(state.code, 0, 'Daemon shutdown failed')
  }
  try {
    const data = join(root, 'data')
    let daemon = await start(old, data, 'run-old')
    const scope = await api(daemon.run, daemon.run.adminToken, 'POST', '/v1/scopes', { id: 'upgrade-rehearsal' })
    const register = (id, connectTo = []) => api(daemon.run, scope.scopeToken, 'POST', '/v1/agents', { id, displayName: id, connectTo, leaseMs: 3600000 })
    const a = await register('a')
    const b = await register('b', ['a'])
    const call = (token, method, path, body) => api(daemon.run, token, method, path, body)
    for (const agent of [a, b]) await call(agent.agentToken, 'PATCH', '/v1/me/heartbeat', { lifecycle: 'ready', ready: true, leaseMs: 3600000 })
    const send = { to: 'b', body: 'preserve outstanding request', mode: 'request', idempotencyKey: 'upgrade-request' }
    const message = await call(a.agentToken, 'POST', '/v1/messages', send)
    const reservation = await call(b.agentToken, 'POST', '/v1/inbox/reserve', { limit: 10, waitMs: 0 })
    const batch = await call(b.agentToken, 'POST', `/v1/inbox/${reservation.id}/commit`, {})
    assert.equal(batch[0].id, message.messageId)
    const parent = await call(a.agentToken, 'POST', '/v1/tasks', { description: 'completed dependency' })
    await call(a.agentToken, 'POST', `/v1/tasks/${parent.id}/claim`, {})
    await call(a.agentToken, 'POST', `/v1/tasks/${parent.id}/complete`, { note: 'done before upgrade' })
    const task = await call(a.agentToken, 'POST', '/v1/tasks', { description: 'preserve claimed task', dependencies: [parent.id] })
    await call(b.agentToken, 'POST', `/v1/tasks/${task.id}/claim`, {})
    const escalation = await call(b.agentToken, 'POST', '/v1/escalations', { question: 'Awaiting approval', options: ['yes', 'no'] })
    const verifyBaseline = async () => {
      const agents = await call(scope.scopeToken, 'GET', '/v1/agents')
      assert.equal(agents.find(agent => agent.id === 'b').executionId, b.executionId)
      const receipt = await call(a.agentToken, 'GET', `/v1/messages/${message.messageId}`)
      assert.equal(receipt.state, 'delivered')
      assert.ok(!receipt.responseMessageId)
      assert.equal((await call(a.agentToken, 'POST', '/v1/messages', send)).messageId, message.messageId, 'Idempotency binding lost')
      const tasks = await call(a.agentToken, 'GET', '/v1/tasks')
      const restored = tasks.find(value => value.id === task.id)
      assert.equal(restored.status, 'claimed')
      assert.equal(restored.claimedBy, 'b')
      assert.equal(restored.description, task.description)
      assert.deepEqual(restored.dependencies, [parent.id])
      assert.equal((await call(b.agentToken, 'GET', `/v1/escalations/${escalation.id}`)).status, 'pending')
    }
    await stop(daemon)
    daemon = await start(candidate, data, 'run-candidate')
    assert.ok((await call('', 'GET', '/health')).features?.includes('session-retirement'))
    await verifyBaseline()
    await call(b.agentToken, 'POST', '/v1/messages', { to: 'a', mode: 'response', responseTo: message.messageId, body: 'continued after migration', idempotencyKey: 'upgrade-response' })
    assert.ok((await call(a.agentToken, 'GET', `/v1/messages/${message.messageId}`)).responseMessageId)
    await call(b.agentToken, 'POST', `/v1/tasks/${task.id}/complete`, { note: 'completed after migration' })
    await call(scope.scopeToken, 'POST', `/v1/scope/escalations/${escalation.id}/resolve`, { answer: 'yes' })
    await stop(daemon)

    // Test refusal on a COPY, never let the old binary touch the upgraded source.
    const probeData = join(root, 'schema9-probe')
    mkdirSync(probeData, { mode: 0o700 })
    copyFileSync(join(data, 'bus.db'), join(probeData, 'bus.db'))
    assert.throws(() => execFileSync(old, ['start'], {
      env: env(probeData, join(root, 'run-probe')), encoding: 'utf8', timeout: 10_000, stdio: ['ignore', 'pipe', 'pipe']
    }), error => error.status !== null && error.status !== 0 && /schema.*9|9.*schema/i.test(error.stderr ?? ''), 'rc.4 must explicitly reject schema 9, not just time out')

    const backups = readdirSync(data).filter(name => name.startsWith('bus.db.schema2-backup-'))
    assert.equal(backups.length, 1, 'Expected one automatic rc.4 snapshot')
    const rollbackData = join(root, 'rollback')
    mkdirSync(rollbackData, { mode: 0o700 })
    copyFileSync(join(data, backups[0]), join(rollbackData, 'bus.db'))
    chmodSync(join(rollbackData, 'bus.db'), 0o600)
    daemon = await start(old, rollbackData, 'run-rollback')
    await verifyBaseline()
    await stop(daemon)
    passed = true
    const sha256 = binary => createHash('sha256').update(readFileSync(binary)).digest('hex')
    console.log(JSON.stringify({ result: 'passed', old: version(old), candidate: candidateVersion, oldSHA256: sha256(old), candidateSHA256: sha256(candidate), checks: ['original credentials and executions', 'outstanding request and idempotency', 'task dependency and claim', 'pending human escalation', 'continued work after migration', 'rc.4 rejects schema 9 copy', 'automatic backup restores pre-upgrade work'] }, null, 2))
  } finally {
    for (const state of children) {
      if (!state.closed) {
        state.child.kill('SIGKILL')
        await waitClosed(state).catch(() => {})
      }
    }
    if (passed && children.every(state => state.closed)) rmSync(root, { recursive: true })
    else console.error(`Rehearsal fixtures retained at ${root}. They contain synthetic credentials; do not upload the directory.`)
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  rehearse().catch(error => { console.error(`Upgrade rehearsal failed: ${error.message}`); process.exitCode = 1 })
}
