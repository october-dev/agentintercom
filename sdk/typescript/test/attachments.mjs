import assert from 'node:assert/strict'
import { createHash, randomBytes } from 'node:crypto'
import { createServer } from 'node:http'
import { once } from 'node:events'
import { test } from 'node:test'
import { AgentIntercomClient, BusError } from '../dist/index.js'

const ID = 'att_fixture'

// Serves `bytes` in readAttachment chunks the way the Bus does. `alter` may
// rewrite a chunk to simulate a corrupted, truncated or inconsistent server.
async function fixture(bytes, alter = (chunk) => chunk, onRequest = () => {}) {
  const attachment = {
    id: ID, name: 'file.bin', mediaType: 'application/octet-stream', size: bytes.length,
    sha256: createHash('sha256').update(bytes).digest('hex'), authorKind: 'agent', authorId: 'a',
    createdAt: '2026-11-10T00:00:00Z', uri: `october-bus://attachments/${ID}`
  }
  const requests = []
  const server = createServer((request, response) => {
    const url = new URL(request.url, 'http://fixture')
    const offset = Number(url.searchParams.get('offset'))
    const limit = Number(url.searchParams.get('limit'))
    requests.push({ path: url.pathname, offset, limit })
    const data = bytes.subarray(offset, offset + limit)
    const end = offset + data.length
    const chunk = alter(
      { attachment: { ...attachment }, offset, dataBase64: Buffer.from(data).toString('base64'), ...(end < bytes.length ? { nextOffset: end } : {}) },
      requests.length
    )
    response.writeHead(200, { 'content-type': 'application/json' })
    response.end(JSON.stringify({ ok: true, result: chunk }))
    onRequest(requests.length)
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  const client = new AgentIntercomClient(`http://127.0.0.1:${server.address().port}`, 'token')
  return { client, requests, attachment, close: () => server.close() }
}

async function rejectsWith(promise, pattern) {
  await assert.rejects(promise, (error) => error instanceof BusError && error.code === 'INTERNAL' && pattern.test(error.message))
}

test('downloads a file larger than one chunk and verifies it', async () => {
  const bytes = randomBytes(1_200_000)
  const { client, requests, attachment, close } = await fixture(bytes)
  try {
    const result = await client.downloadAttachment(ID)
    assert.deepEqual(Buffer.from(result.data), bytes)
    assert.equal(result.attachment.sha256, attachment.sha256)
    assert.deepEqual(requests.map((r) => r.offset), [0, 524288, 1048576])
    assert(requests.every((r) => r.path === `/v1/attachments/${ID}` && r.limit === 524288))
  } finally { close() }
})

test('downloads a single-chunk file and an empty file', async () => {
  const small = await fixture(Buffer.from('hello world'))
  try {
    const result = await small.client.downloadAttachment(ID)
    assert.equal(Buffer.from(result.data).toString(), 'hello world')
    assert.equal(small.requests.length, 1)
  } finally { small.close() }
  const empty = await fixture(Buffer.alloc(0))
  try {
    assert.equal((await empty.client.downloadAttachment(ID)).data.length, 0)
  } finally { empty.close() }
})

test('rejects corrupted content with an unchanged size', async () => {
  const { client, close } = await fixture(randomBytes(600_000), (chunk, n) => {
    if (n !== 2) return chunk
    const data = Buffer.from(chunk.dataBase64, 'base64')
    data[0] ^= 0xff
    return { ...chunk, dataBase64: data.toString('base64') }
  })
  try { await rejectsWith(client.downloadAttachment(ID), /SHA-256 does not match/) } finally { close() }
})

test('rejects truncated data', async () => {
  const { client, close } = await fixture(randomBytes(600_000), (chunk) => {
    const { nextOffset, ...rest } = chunk
    return rest
  })
  try { await rejectsWith(client.downloadAttachment(ID), /received 524288 of 600000 bytes/) } finally { close() }
})

test('rejects metadata that changes between chunks', async () => {
  const cases = {
    size: (a) => ({ ...a, size: a.size + 1 }),
    checksum: (a) => ({ ...a, sha256: 'a'.repeat(64) }),
    id: (a) => ({ ...a, id: 'att_other' })
  }
  for (const [name, change] of Object.entries(cases)) {
    const { client, close } = await fixture(randomBytes(600_000), (chunk, n) => (n === 2 ? { ...chunk, attachment: change(chunk.attachment) } : chunk))
    try { await rejectsWith(client.downloadAttachment(ID), /changed/) } catch (error) { throw new Error(`${name}: ${error.message}`) } finally { close() }
  }
})

test('rejects bad offsets and continuations', async () => {
  const cases = [
    [(chunk, n) => (n === 2 ? { ...chunk, offset: chunk.offset + 1 } : chunk), /ID or offset changed/],
    [(chunk, n) => (n === 1 ? { ...chunk, nextOffset: chunk.nextOffset + 10 } : chunk), /invalid continuation/],
    [(chunk, n) => (n === 1 ? { ...chunk, dataBase64: '', nextOffset: 0 } : chunk), /invalid continuation/],
    [(chunk) => ({ ...chunk, dataBase64: Buffer.alloc(600_001).toString('base64') }), /more bytes than the attachment size/],
    [(chunk) => ({ ...chunk, dataBase64: '***' }), /not valid base64/]
  ]
  for (const [alter, pattern] of cases) {
    const { client, close } = await fixture(randomBytes(600_000), alter)
    try { await rejectsWith(client.downloadAttachment(ID), pattern) } finally { close() }
  }
})

test('refuses oversized or malformed metadata after one request', async () => {
  for (const [change, pattern] of [
    [(a) => ({ ...a, size: 16 * 1024 * 1024 + 1 }), /outside the 16 MiB/],
    [(a) => ({ ...a, size: -1 }), /outside the 16 MiB/],
    [(a) => ({ ...a, sha256: 'not-a-checksum' }), /checksum is malformed/]
  ]) {
    const { client, requests, close } = await fixture(randomBytes(100_000), (chunk) => ({ ...chunk, attachment: change(chunk.attachment) }))
    try {
      await rejectsWith(client.downloadAttachment(ID), pattern)
      assert.equal(requests.length, 1)
    } finally { close() }
  }
})

test('stops requesting chunks after cancellation', async () => {
  const before = new AbortController()
  before.abort(new Error('stopped early'))
  const idle = await fixture(randomBytes(100_000))
  try {
    await rejectsWith(idle.client.downloadAttachment(ID, { signal: before.signal }), /stopped early/)
    assert.equal(idle.requests.length, 0)
  } finally { idle.close() }

  const during = new AbortController()
  const busy = await fixture(randomBytes(1_200_000), (chunk) => chunk, (n) => { if (n === 1) during.abort(new Error('stopped midway')) })
  try {
    await rejectsWith(busy.client.downloadAttachment(ID, { signal: during.signal }), /stopped midway/)
    assert.equal(busy.requests.length, 1)
  } finally { busy.close() }
})
