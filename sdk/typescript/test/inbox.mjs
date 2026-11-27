import assert from 'node:assert/strict'
import { createServer } from 'node:http'
import { once } from 'node:events'
import { test } from 'node:test'
import { AgentIntercomClient } from '../dist/index.js'

test('pullInbox releases its reservation when the commit fails', async () => {
  const paths = []
  const server = createServer((request, response) => {
    paths.push(request.url)
    response.setHeader('content-type', 'application/json')
    if (request.url === '/v1/inbox/reserve') return response.end(JSON.stringify({ ok: true, result: { id: 'res_1', messages: [] } }))
    if (request.url === '/v1/inbox/res_1/commit') {
      response.statusCode = 503
      return response.end(JSON.stringify({ ok: false, error: { code: 'BACKPRESSURE', message: 'try again' } }))
    }
    response.end(JSON.stringify({ ok: true, result: {} }))
  })
  server.listen(0, '127.0.0.1')
  await once(server, 'listening')
  try {
    const client = new AgentIntercomClient(`http://127.0.0.1:${server.address().port}`, 'token')
    await assert.rejects(client.pullInbox(10))
    assert.deepEqual(paths, ['/v1/inbox/reserve', '/v1/inbox/res_1/commit', '/v1/inbox/res_1/release'])
  } finally {
    server.close()
  }
})
