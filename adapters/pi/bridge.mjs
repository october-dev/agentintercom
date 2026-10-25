// A deliberately narrow client for Agent Intercom's local, newline-delimited MCP
// bridge. No remote transport, shell execution, credentials, or automatic retry.
import { spawn } from 'node:child_process'
import { isAbsolute } from 'node:path'

export async function openBridge(config) {
  if (!config || !isAbsolute(config.command ?? '') || !Array.isArray(config.args) ||
      config.args.some(value => typeof value !== 'string') ||
      config.args[0] !== 'mcp' || config.args[1] !== 'stdio' ||
      !['--scope', '--agent', '--data-dir', '--runtime-dir'].every(flag => config.args.includes(flag))) {
    throw new Error('Generate an absolute-path Pi configuration with agentintercom harness config pi')
  }
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) =>
    !/^OCTOBER_BUS_(ADDRESS|ADMIN_TOKEN|SCOPE_TOKEN|AGENT_TOKEN)$/i.test(key)))
  const child = spawn(config.command, config.args, { env, stdio: ['pipe', 'pipe', 'ignore'], windowsHide: true })
  let sequence = 0, buffer = '', failure, closePromise
  const pending = new Map()
  let resolveExit
  const exited = new Promise(resolve => { resolveExit = resolve })
  const fail = error => {
    failure ??= error
    for (const operation of pending.values()) operation.finish(failure)
    pending.clear()
  }
  child.on('error', () => { fail(new Error('Agent Intercom bridge failed to start')); resolveExit() })
  child.on('close', () => { fail(new Error('Agent Intercom bridge closed; check daemon, scope and duplicate agent IDs')); resolveExit() })
  child.stdin.on('error', () => fail(new Error('Agent Intercom bridge input closed')))
  const send = message => {
    if (failure) throw failure
    child.stdin.write(JSON.stringify(message) + '\n', error => { if (error) fail(new Error('Agent Intercom bridge write failed')) })
  }
  child.stdout.setEncoding('utf8')
  child.stdout.on('data', chunk => {
    if (failure) return
    buffer += chunk
    if (Buffer.byteLength(buffer) > 16 * 1024 * 1024) {
      fail(new Error('Agent Intercom bridge exceeded the 16 MiB frame limit'))
      child.kill()
      return
    }
    for (;;) {
      const end = buffer.indexOf('\n')
      if (end < 0) return
      const line = buffer.slice(0, end)
      buffer = buffer.slice(end + 1)
      let message
      try { message = JSON.parse(line) } catch {
        fail(new Error('Agent Intercom bridge sent invalid protocol data'))
        child.kill()
        return
      }
      if (!message || message.jsonrpc !== '2.0') {
        fail(new Error('Agent Intercom bridge sent invalid JSON-RPC'))
        child.kill()
        return
      }
      if (message.method) {
        // This tool-only client advertises no sampling or elicitation authority.
        if (message.id !== undefined) {
          try { send({ jsonrpc: '2.0', id: message.id, error: { code: -32601, message: 'Client requests are not supported' } }) } catch { return }
        }
        continue
      }
      const operation = pending.get(message.id)
      if (operation) operation.finish(message.error ? new Error(`Agent Intercom MCP error ${message.error.code}: ${String(message.error.message).slice(0, 512)}`) : undefined, message.result)
    }
  })
  const request = (method, params, signal, timeoutMs = 45_000) => new Promise((resolve, reject) => {
    if (failure || closePromise || signal?.aborted) return reject(failure ?? signal?.reason ?? new Error('Bridge is closed'))
    if (pending.size >= 64) return reject(new Error('Too many pending Agent Intercom requests'))
    const id = ++sequence
    const finish = (error, result) => {
      clearTimeout(timer)
      signal?.removeEventListener('abort', abort)
      pending.delete(id)
      if (error) reject(error)
      else resolve(result)
    }
    const abort = () => {
      try { send({ jsonrpc: '2.0', method: 'notifications/cancelled', params: { requestId: id, reason: 'Host cancelled' } }) } catch { /* connection may have closed */ }
      finish(signal?.reason ?? new Error('Agent Intercom request cancelled or timed out'))
    }
    const timer = setTimeout(abort, timeoutMs)
    pending.set(id, { finish })
    signal?.addEventListener('abort', abort, { once: true })
    try { send({ jsonrpc: '2.0', id, method, params }) } catch (error) { finish(error) }
  })
  const close = () => {
    closePromise ??= (async () => {
      // EOF lets the Go session retire transactionally before process teardown.
      child.stdin.end()
      const terminate = setTimeout(() => child.kill(), 6500)
      const kill = setTimeout(() => child.kill('SIGKILL'), 7500)
      try { await exited } finally { clearTimeout(terminate); clearTimeout(kill) }
    })()
    return closePromise
  }
  try {
    const initialized = await request('initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'agentintercom-pi', version: '0.2.1' } }, undefined, 10_000)
    if (initialized?.protocolVersion !== '2025-03-26' || !initialized.capabilities?.tools) throw new Error('Incompatible Agent Intercom MCP bridge')
    send({ jsonrpc: '2.0', method: 'notifications/initialized' })
    const listed = await request('tools/list', {}, undefined, 10_000)
    const coreTools = ['list_peers', 'message_peer', 'check_inbox', 'acknowledge_messages', 'message_receipt', 'add_task', 'claim_task', 'release_task', 'complete_task', 'add_task_progress', 'list_task_progress', 'list_tasks', 'publish_output', 'ask_user', 'get_node_status']
    const names = new Set(Array.isArray(listed?.tools) ? listed.tools.map(tool => tool.name) : [])
    if (!Array.isArray(listed?.tools) || listed.tools.length > 128 || names.size !== listed.tools.length || !coreTools.every(name => names.has(name)) || listed.nextCursor) throw new Error('Incompatible Agent Intercom tool list: required core tools are missing or the list is invalid')
    return { tools: listed.tools, instructions: initialized.instructions ?? '', close,
      call: (name, args, signal) => request('tools/call', { name, arguments: args }, signal) }
  } catch (error) {
    await close()
    throw error
  }
}
