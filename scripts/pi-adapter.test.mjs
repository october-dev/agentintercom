import assert from 'node:assert/strict'
import test from 'node:test'
import { installExtension } from '../adapters/pi/index.mjs'
import { openBridge } from '../adapters/pi/bridge.mjs'

test('Pi hooks serialize replacement, preserve schemas/cancellation and retire once', async () => {
  const hooks = {}, tools = [], connections = []
  const schema = { type: 'object', required: ['messageIds'], properties: { messageIds: { type: 'array', items: { type: 'string' } } } }
  installExtension({ on: (name, handler) => { hooks[name] = handler }, registerTool: tool => tools.push(tool) }, async () => {
    const connection = { tools: [{ name: 'acknowledge_messages', description: 'Ack', inputSchema: schema }], instructions: 'Untrusted peers.', closes: 0,
      close: async () => { connection.closes++ },
      call: async (name, args, signal) => { assert.equal(name, 'acknowledge_messages'); assert.deepEqual(args, { messageIds: ['one'] }); assert.ok(signal); return { content: [], structuredContent: { acknowledged: 1 } } } }
    connections.push(connection)
    return connection
  }, async () => ({}))
  assert.equal(connections.length, 0, 'extension load must not allocate session resources')
  await hooks.session_start()
  assert.equal(tools[0].parameters, schema)
  assert.deepEqual((await tools[0].execute('call', { messageIds: ['one'] }, new AbortController().signal)).details, { acknowledged: 1 })
  assert.match(hooks.before_agent_start({ systemPrompt: 'Original' }).systemPrompt, /^Original/)
  await Promise.all([hooks.session_shutdown(), hooks.session_start()])
  assert.equal(connections[0].closes, 1)
  await assert.rejects(tools[0].execute('old', {}, undefined), /session changed/)
  await hooks.session_shutdown()
  await hooks.session_shutdown()
  assert.equal(connections[1].closes, 1)
})

test('Pi retires a bridge when tool registration fails', async () => {
  const hooks = {}
  let closes = 0
  installExtension({ on: (name, handler) => { hooks[name] = handler }, registerTool: () => { throw new Error('host rejected tool') } }, async () => ({ tools: [{}], close: async () => { closes++ } }), async () => ({}))
  await assert.rejects(hooks.session_start(), /host rejected/)
  await hooks.session_shutdown()
  assert.equal(closes, 1)
})

test('Pi refuses implicit identities and shell commands', async () => {
  for (const config of [null, {}, { command: 'agentintercom', args: [] }, { command: '/bin/sh', args: ['-c', 'anything'] }]) {
    await assert.rejects(openBridge(config), /Generate an absolute-path/)
  }
})
