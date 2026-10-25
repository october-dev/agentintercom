import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { readFile, writeFile } from 'node:fs/promises'
import { join } from 'node:path'
import { AgentIntercomAdminClient, AgentIntercomClient, AgentIntercomScopeClient } from '../dist/index.js'
import { openBridge } from '../../../adapters/pi/bridge.mjs'

export async function checkHarnessSetup(binary, run, env, root) {
  const cli = args => execFileSync(binary, args, { env, encoding: 'utf8', timeout: 20_000, stdio: ['ignore', 'pipe', 'pipe'] })
  const scope = JSON.parse(cli(['scope', 'create', 'harness-integration']))
  const owner = new AgentIntercomScopeClient(run.address, scope.scopeToken)
  const admin = new AgentIntercomAdminClient(run.address, run.adminToken)
  let bridge
  try {
    const config = JSON.parse(cli(['harness', 'config', 'pi', '--scope', scope.scopeId, '--agent', 'pi']))
    assert.ok(!JSON.stringify(config).includes(scope.scopeToken))
    assert.ok(!JSON.stringify(config).includes(run.adminToken))
    const protectedFile = join(root, 'existing-config.json')
    await writeFile(protectedFile, 'user configuration')
    assert.throws(() => cli(['harness', 'config', 'pi', '--scope', scope.scopeId, '--agent', 'pi', '--output', protectedFile]))
    assert.equal(await readFile(protectedFile, 'utf8'), 'user configuration')
    const peer = await owner.registerAgent({ id: 'peer', displayName: 'Peer', leaseMs: 30_000 })
    const client = new AgentIntercomClient(run.address, peer.agentToken)
    bridge = await openBridge(config)
    assert.equal(bridge.tools.length, 23)
    cli(['link', '--scope', scope.scopeId, 'pi', 'peer'])
    const call = async (name, args) => {
      const result = await bridge.call(name, args)
      assert.ok(!result.isError, JSON.stringify(result))
      return result.structuredContent
    }
    const receipt = await client.sendMessage({ to: 'pi', mode: 'request', body: 'Native transport check' })
    assert.equal((await call('check_inbox', {})).messages[0].id, receipt.messageId)
    await call('acknowledge_messages', { messageIds: JSON.stringify([receipt.messageId]) })
    const reply = await call('message_peer', { peer: 'peer', message: 'Checked', mode: 'response', responseTo: receipt.messageId })
    assert.equal((await call('message_receipt', { messageId: receipt.messageId })).responseMessageId, reply.messageId)
    const task = await call('add_task', { title: 'Release on EOF' })
    await call('claim_task', { taskId: task.id })
    const abort = new AbortController()
    const waiting = bridge.call('check_inbox', { waitMs: 25_000 }, abort.signal)
    abort.abort(new Error('Model turn cancelled'))
    await assert.rejects(waiting, /cancelled/)
    assert.equal((await call('get_node_status', {})).agent.id, 'pi')
    await bridge.close()
    bridge = undefined
    assert.equal((await owner.listAgents()).find(agent => agent.id === 'pi').reachable, false)
    await client.claimTask(task.id)
    // Probe an editor without invoking its executable or requiring model access.
    const diagnosis = JSON.parse(cli(['doctor', '--harness', 'continue', '--scope', scope.scopeId, '--json']))
    assert.equal(diagnosis.healthy, true)
    assert.equal(diagnosis.bridgeTools, 23)
    assert.ok((await owner.listAgents()).filter(agent => agent.id.startsWith('doctor-')).every(agent => !agent.reachable))
  } finally {
    await bridge?.close()
    await admin.deleteScope(scope.scopeId)
  }
}
