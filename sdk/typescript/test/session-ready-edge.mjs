import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import {
  OctoberBusAdminClient,
  OctoberBusAgentSession,
  OctoberBusClient,
  OctoberBusScopeClient,
} from '../dist/index.js'

const binary = process.env.OCTOBER_BUS_BINARY
if (!binary) throw new Error('OCTOBER_BUS_BINARY is required')

const root = await mkdtemp(join(tmpdir(), 'october-bus-ready-edge-'))
const dataDir = join(root, 'data')
const runtimeDir = join(root, 'run')
const runFile = join(runtimeDir, 'bus.json')
let child = spawn(binary, ['start'], {
  env: {
    ...process.env,
    OCTOBER_BUS_DATA_DIR: dataDir,
    OCTOBER_BUS_RUNTIME_DIR: runtimeDir
  },
  stdio: ['ignore', 'pipe', 'pipe']
})

let stderr = ''
child.stderr.setEncoding('utf8')
child.stderr.on('data', (value) => {
  stderr += value
})

async function readRunFile() {
  const deadline = Date.now() + 10_000
  while (Date.now() < deadline) {
    if (child.exitCode !== null) throw new Error(`October Bus exited early: ${stderr}`)
    try {
      return JSON.parse(await readFile(runFile, 'utf8'))
    } catch {
      await new Promise((resolve) => setTimeout(resolve, 50))
    }
  }
  throw new Error(`October Bus did not start: ${stderr}`)
}

async function startBus() {
  child = spawn(binary, ['start'], {
    env: {
      ...process.env,
      OCTOBER_BUS_DATA_DIR: dataDir,
      OCTOBER_BUS_RUNTIME_DIR: runtimeDir
    },
    stdio: ['ignore', 'pipe', 'pipe']
  })
  stderr = ''
  child.stderr.setEncoding('utf8')
  child.stderr.on('data', (value) => { stderr += value })
  return readRunFile()
}

let session

try {
  const run = await readRunFile()
  const admin = new OctoberBusAdminClient(run.address, run.adminToken)
  const scope = await admin.createScope({ id: 'ready-edge' })
  const owner = new OctoberBusScopeClient(run.address, scope.scopeToken)

  // Host starts NOT ready. Long heartbeat interval so background beats don't
  // interfere with the transition tests below.
  session = await OctoberBusAgentSession.start({
    address: run.address,
    scopeToken: scope.scopeToken,
    registration: { id: 'host', displayName: 'Host', leaseMs: 60_000 },
    heartbeatIntervalMs: 30_000,
    initialReady: false
  })

  // --- 1. Successful false→true fires the wake signal ---
  const wakeBefore = session.wake
  await session.setState('ready', true)
  assert.equal(wakeBefore.aborted, true, 'false→true must abort the old wake signal')
  assert.equal(session.wake.aborted, false, 'wake signal must be live after a successful transition')

  // --- 2. true→false does NOT fire the wake signal ---
  const wakeAtTrue = session.wake
  await session.setState('working', false)
  assert.equal(wakeAtTrue.aborted, false, 'true→false must not fire the wake signal')
  assert.equal(session.wake.aborted, false, 'wake signal must remain live')

  // --- 3. Failed false→true does NOT fire the wake signal ---
  // Kill the server to force a heartbeat failure. The session stays ready=false
  // so the false→true edge can be retried.
  child.kill('SIGTERM')
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    new Promise((resolve) => setTimeout(resolve, 5_000))
  ])
  if (child.exitCode === null) child.kill('SIGKILL')

  const wakeBeforeFail = session.wake
  await assert.rejects(
    () => session.setState('ready', true),
    (error) => {
      assert(error instanceof Error)
      return true
    }
  )
  assert.equal(wakeBeforeFail.aborted, false, 'a failed false→true must not fire the wake signal')

  // --- 4. Successful retry: restart the server, then false→true again ---
  // The data dir persists, so the agent's execution and scope survive restart.
  const newRun = await startBus()
  // Update the session's client to the new address (port may change).
  session.client = new OctoberBusClient(newRun.address, session.registration.agentToken)
  const wakeBeforeRetry = session.wake
  await session.setState('ready', true)
  assert.equal(wakeBeforeRetry.aborted, true, 'successful retry false→true must fire the wake signal')
  assert.equal(session.wake.aborted, false, 'wake signal must be live after a successful retry')
} finally {
  await session?.close().catch(() => {})
  if (child.exitCode === null) child.kill('SIGKILL')
  await rm(root, { recursive: true, force: true })
}