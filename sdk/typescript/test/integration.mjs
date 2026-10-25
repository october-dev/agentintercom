import assert from 'node:assert/strict'
import { spawn } from 'node:child_process'
import { mkdtemp, readFile, rm } from 'node:fs/promises'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { checkHarnessSetup } from './harness-integration.mjs'
import {
  OctoberBusAdminClient,
  OctoberBusAgentSession,
  OctoberBusOutputClient,
  OctoberBusScopeClient,
  newIdempotencyKey,
  withClaimedTask
} from '../dist/index.js'

const binary = process.env.OCTOBER_BUS_BINARY
if (!binary) throw new Error('OCTOBER_BUS_BINARY is required')

const root = await mkdtemp(join(tmpdir(), 'october-bus-sdk-'))
const dataDir = join(root, 'data')
const runtimeDir = join(root, 'run')
const runFile = join(runtimeDir, 'bus.json')
const child = spawn(binary, ['start'], {
  env: {
    ...process.env,
    OCTOBER_BUS_DATA_DIR: dataDir,
    OCTOBER_BUS_RUNTIME_DIR: runtimeDir
  },
  stdio: ['ignore', 'pipe', 'pipe']
})

let stderr = ''
let plannerSession
let reviewerSession
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

try {
  const run = await readRunFile()
  await checkHarnessSetup(binary, run, { ...process.env, OCTOBER_BUS_DATA_DIR: dataDir, OCTOBER_BUS_RUNTIME_DIR: runtimeDir }, root)
  const admin = new OctoberBusAdminClient(run.address, run.adminToken)
  const health = await admin.health()
  assert.equal(health.protocolVersion, '0.1')
  assert.equal(typeof health.runtimeVersion, 'string')
  assert.deepEqual(health.storage, { backend: 'sqlite', status: 'available' })
  const liveness = await admin.liveness()
  assert.equal(liveness.status, 'alive')
  const scope = await admin.createScope({ id: 'typescript-integration' })
  const owner = new OctoberBusScopeClient(run.address, scope.scopeToken)
  plannerSession = await OctoberBusAgentSession.start({
    address: run.address,
    scopeToken: scope.scopeToken,
    registration: { id: 'planner', displayName: 'Planner', leaseMs: 30_000 },
    heartbeatIntervalMs: 10
  })
  reviewerSession = await OctoberBusAgentSession.start({
    address: run.address,
    scopeToken: scope.scopeToken,
    registration: { id: 'reviewer', displayName: 'Reviewer', connectTo: ['planner'], leaseMs: 30_000 },
    heartbeatIntervalMs: 10
  })
  const publication = await owner.createAgentCardPublication({ agentId: 'reviewer' })
  assert.equal(publication.enabled, true)
  assert.match(publication.id, /^pub_[0-9a-f]{32}$/)
  const cardResponse = await fetch(publication.cardUrl)
  assert.equal(cardResponse.status, 200)
  const card = await cardResponse.json()
  assert.equal(card.name, 'Reviewer')
  assert.equal(card.supportedInterfaces[0].url, publication.interfaceUrl)
  const disabledPublication = await owner.setAgentCardPublicationEnabled(publication.id, false)
  assert.equal(disabledPublication.enabled, false)
  assert.equal((await fetch(publication.cardUrl)).status, 404)
  const enabledPublication = await owner.setAgentCardPublicationEnabled(publication.id, true)
  assert.equal(enabledPublication.enabled, true)
  assert.equal(enabledPublication.cardUrl, publication.cardUrl)
  const issuedPrincipal = await owner.createA2APrincipal({
    publicationId: publication.id,
    label: 'Integration caller'
  })
  assert.match(issuedPrincipal.principal.id, /^cred_[0-9a-f]{32}$/)
  assert.match(issuedPrincipal.credential, new RegExp(`^${issuedPrincipal.principal.id}\\.`))
  const principals = await owner.listA2APrincipals()
  assert.equal(principals.length, 1)
  assert.equal(principals[0].id, issuedPrincipal.principal.id)
  assert.equal(Object.hasOwn(principals[0], 'credential'), false)
  const scopedAccess = await fetch(`${run.address}/v1/agents`, {
    headers: { authorization: `Bearer ${issuedPrincipal.credential}` }
  })
  assert.equal(scopedAccess.status, 403)
  const disabledPrincipal = await owner.setA2APrincipalEnabled(issuedPrincipal.principal.id, false)
  assert.equal(disabledPrincipal.enabled, false)
  const rotatedPrincipal = await owner.rotateA2APrincipal(issuedPrincipal.principal.id)
  assert.equal(rotatedPrincipal.principal.enabled, false)
  assert.notEqual(rotatedPrincipal.credential, issuedPrincipal.credential)
  const enabledPrincipal = await owner.setA2APrincipalEnabled(issuedPrincipal.principal.id, true)
  assert.equal(enabledPrincipal.enabled, true)
  const agentsBeforeReady = await owner.listAgents()
  assert.equal(agentsBeforeReady.every((agent) => !agent.ready), true)
  await plannerSession.setState('ready', true)
  await reviewerSession.setState('ready', true)
  const planner = plannerSession.client
  const reviewer = reviewerSession.client

  const fileBytes = new Uint8Array([0, 255, 10, 27, 42])
  const upload = { name: 'shared.bin', mediaType: 'application/octet-stream', idempotencyKey: 'sdk-file' }
  const file = await planner.uploadAttachment(upload, fileBytes)
  assert.equal((await planner.uploadAttachment(upload, fileBytes)).id, file.id)
  assert.equal((await reviewer.listAttachments()).attachments[0].id, file.id)
  const firstChunk = await reviewer.readAttachment(file.id, 0, 2)
  const secondChunk = await reviewer.readAttachment(file.id, firstChunk.nextOffset, 512)
  assert.deepEqual(Buffer.concat([Buffer.from(firstChunk.dataBase64, 'base64'), Buffer.from(secondChunk.dataBase64, 'base64')]), Buffer.from(fileBytes))
  await assert.rejects(reviewer.deleteAttachment(file.id), error => error.code === 'PERMISSION_DENIED')
  await planner.deleteAttachment(file.id)
  await assert.rejects(reviewer.readAttachment(file.id), error => error.code === 'NOT_FOUND')

  const node = await planner.nodeStatus()
  assert.equal(node.identity.agentId, 'planner')
  assert.equal(node.identity.executionId, plannerSession.registration.executionId)
  assert.equal(node.agent.id, 'planner')
  const outputStream = await owner.createOutputStream({
    name: 'site-preview',
    retentionLimit: 2,
    publisherAgentIds: ['reviewer']
  })
  const outputReader = await owner.createOutputPrincipal({
    streamId: outputStream.id,
    label: 'Preview page',
    permissions: ['read']
  })
  await reviewer.publishOutput(outputStream.id, {
    contentType: 'text/plain',
    value: 'building'
  })
  await reviewer.publishOutput(outputStream.id, {
    contentType: 'application/json',
    value: { status: 'ready', url: 'https://example.test/preview' }
  })
  const outputs = new OctoberBusOutputClient(run.address, outputReader.credential)
  assert.deepEqual((await outputs.latest(outputStream.id)).value, {
    status: 'ready',
    url: 'https://example.test/preview'
  })
  assert.equal((await outputs.history(outputStream.id)).values.length, 2)
  const initialEvents = await owner.events({ limit: 100 })
  assert.equal(initialEvents.events.length > 0, true)
  assert.equal(initialEvents.resyncRequired, false)
  const eventIterator = owner.watchEvents({
    after: initialEvents.nextRevision,
    waitMs: 2_000,
    timeoutMs: 3_000
  })
  const nextEvents = eventIterator.next()
  await planner.heartbeat('working', true)
  const eventResult = await nextEvents
  assert.equal(eventResult.done, false)
  assert.equal(eventResult.value.events.some((event) => event.type === 'agent.lifecycle_changed'), true)
  await eventIterator.return()
  const peers = await planner.listPeers()
  assert.equal(peers.length, 1)
  assert.equal(peers[0].id, 'reviewer')
  const message = { to: 'reviewer', mode: 'request', body: 'Review this', idempotencyKey: newIdempotencyKey() }
  const receipt = await planner.sendMessage(message)
  const retry = await planner.sendMessage(message)
  assert.equal(retry.messageId, receipt.messageId)
  const messages = await reviewer.pullInbox()
  assert.equal(messages.length, 1)
  assert.equal(messages[0].id, receipt.messageId)
  assert.equal(await reviewer.acknowledgeMessages([messages[0].id]), 1)
  const waitingInbox = reviewer.pullInbox(50, { waitMs: 2_000, timeoutMs: 3_000 })
  await new Promise((resolve) => setTimeout(resolve, 50))
  const waitingReceipt = await planner.sendMessage({
    to: 'reviewer',
    body: 'Wake the waiting reviewer',
    idempotencyKey: newIdempotencyKey()
  })
  const waitingMessages = await waitingInbox
  assert.equal(waitingMessages.length, 1)
  assert.equal(waitingMessages[0].id, waitingReceipt.messageId)
  assert.equal(await reviewer.acknowledgeMessages([waitingMessages[0].id]), 1)
  const task = await owner.addTask({ title: 'Review integration' })
  assert.equal(task.createdBy, null)
  assert.equal(task.ready, true)
  assert.deepEqual((await owner.listTasks({ ready: true })).map((value) => value.id), [task.id])
  const completed = await withClaimedTask(reviewer, task.id, async () => {
    await reviewer.addTaskProgress(task.id, { kind: 'progress', text: 'Review started' })
    return 'reviewed'
  }, (value) => value)
  assert.equal(completed.task.status, 'done')
  assert.equal(completed.value, 'reviewed')
  const page = await plannerSession.client.taskPage('', 1)
  assert.equal(page.tasks.length, 1)
  assert.equal(page.tasks[0].id, task.id)
  const progress = await owner.listTaskProgress(task.id)
  assert.equal(progress.length, 1)
  assert.equal(progress[0].text, 'Review started')
  const storage = await owner.storageSummary()
  assert.equal(storage.records.some((record) => record.recordType === 'task' && record.state === 'done'), true)
  assert.equal(
    storage.records
      .filter((record) => record.recordType === 'outputValue')
      .reduce((count, record) => count + record.count, 0),
    2
  )
  const before = new Date(Date.now() + 60_000).toISOString()
  const dryRun = await owner.pruneScope({ before })
  assert.equal(dryRun.dryRun, true)
  assert.equal(dryRun.records.tasks, 1)
  assert.equal(dryRun.records.taskProgress, 1)
  const pruned = await owner.pruneScope({ before, execute: true })
  assert.equal(pruned.dryRun, false)
  assert.equal(pruned.records.tasks, 1)
  assert.equal((await owner.events({ after: 0 })).resyncRequired, true)
  await reviewerSession.close()
  await plannerSession.close()
  const agentsAfterClose = await owner.listAgents()
  assert.equal(agentsAfterClose.every((agent) => !agent.reachable && agent.lifecycle === 'offline'), true)
  let backupBytes = 0
  let prefix = Buffer.alloc(0)
  for await (const chunk of admin.backup()) {
    backupBytes += chunk.byteLength
    if (prefix.length < 16) prefix = Buffer.concat([prefix, Buffer.from(chunk)]).subarray(0, 16)
  }
  assert.equal(prefix.toString(), 'SQLite format 3\0')
  assert.ok(backupBytes > 16)
  await assert.rejects(plannerSession.client.heartbeat('ready', true), (error) => error.code === 'UNAUTHENTICATED')
  await plannerSession.client.retire() // idempotent, without reviving authority
  const recovered = await admin.rotateScopeToken(scope.scopeId)
  await assert.rejects(owner.listAgents(), (error) => error.code === 'UNAUTHENTICATED')
  const recoveredOwner = new OctoberBusScopeClient(run.address, recovered.scopeToken)
  assert.equal((await recoveredOwner.listAgents()).length, 2)
  assert.equal((await admin.listScopes()).some((entry) => entry.scopeId === scope.scopeId), true)
  await admin.deleteScope(scope.scopeId)
  await admin.deleteScope(scope.scopeId)
  assert.equal((await admin.listScopes()).length, 0)
} finally {
  await reviewerSession?.close()
  await plannerSession?.close()
  child.kill('SIGTERM')
  await Promise.race([
    new Promise((resolve) => child.once('exit', resolve)),
    new Promise((resolve) => setTimeout(resolve, 5_000))
  ])
  if (child.exitCode === null) child.kill('SIGKILL')
  await rm(root, { recursive: true, force: true })
}
