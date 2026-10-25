#!/usr/bin/env node
// Deployment handshake check; creates/renews the test connector's execution but
// sends no messages. Credentials stay in a private file and never appear in
// arguments, URLs, output, or redirect requests. Node 20+.
import { lstatSync, readFileSync } from 'node:fs'

async function main() {
  const [endpoint, keyFile, ...extra] = process.argv.slice(2)
  if (!endpoint || !keyFile || extra.length) throw new Error('Usage: node scripts/check-hosted-mcp.mjs <https://host/mcp> <private-key-file>')
  const url = new URL(endpoint)
  if (url.protocol !== 'https:' || url.pathname !== '/mcp' || url.username || url.password || url.search || url.hash) {
    throw new Error('Expected an HTTPS /mcp endpoint without credentials, query, or fragment')
  }
  const info = lstatSync(keyFile)
  if (!info.isFile() || info.size > 256 || (process.platform !== 'win32' && (info.mode & 0o077))) {
    throw new Error('API key file must be an owner-only regular file of at most 256 bytes')
  }
  const key = readFileSync(keyFile, 'utf8').trim()
  if (!/^ob[gka]_[A-Za-z0-9_-]{43}$/.test(key)) throw new Error('Expected a gateway key, hosted MCP key, or OAuth access token')
  const legacy = key.startsWith('obg_')
  async function request(path, options = {}) {
    return fetch(new URL(path, url), { ...options, redirect: 'error', signal: AbortSignal.timeout(15_000) })
  }
  const ready = await request('/health/ready')
  if (ready.status === 200) {
    const health = await ready.json()
    if (health.name !== 'october-bus' || health.status !== 'ready' || (!legacy && health.storage?.backend !== 'postgres')) throw new Error('Hosted runtime is not ready with the expected storage')
  } else if (!legacy || ready.status !== 204) throw new Error(`Service is not ready (HTTP ${ready.status})`)
  const denied = await request('/mcp', { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: '{}' })
  if (denied.status !== 401) throw new Error(`Expected unauthenticated MCP to return 401; got ${denied.status}`)
  for (const path of ['/bus/v1/admin/backup', '/v1/admin/backup']) {
    const admin = await request(path)
    if (admin.status !== 404) throw new Error(`Private admin route was not hidden (HTTP ${admin.status})`)
  }
  if (!legacy) {
    const discovery = await request('/.well-known/oauth-protected-resource/mcp')
    if (!discovery.ok || (await discovery.json()).resource !== endpoint) throw new Error('MCP protected-resource discovery does not match the endpoint')
    const app = await request('/app')
    if (!app.ok || !app.headers.get('content-security-policy')?.includes("frame-ancestors 'none'")) throw new Error('Workspace browser route or security policy is unavailable')
    const config = await request('/api/config')
    if (!config.ok || (await config.json()).result?.mcpUrl !== endpoint) throw new Error('Browser configuration does not match the MCP endpoint')
  }
  const headers = { Authorization: `Bearer ${key}`, 'Content-Type': 'application/json', Accept: 'application/json, text/event-stream' }
  async function rpc(id, method, params) {
    const response = await request('/mcp', { method: 'POST', headers, body: JSON.stringify({ jsonrpc: '2.0', id, method, params }) })
    if (!response.ok) throw new Error(`${method} failed (HTTP ${response.status})`)
    const payload = await response.json()
    if (payload.error || !payload.result) throw new Error(`${method} returned an invalid JSON-RPC result`)
    return payload.result
  }
  const init = await rpc(1, 'initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'october-hosted-check', version: '1' } })
  if (init.serverInfo?.name !== 'agentintercom') throw new Error('Endpoint is not the Agent Intercom MCP server')
  headers['MCP-Protocol-Version'] = init.protocolVersion
  const initialized = await request('/mcp', { method: 'POST', headers, body: JSON.stringify({ jsonrpc: '2.0', method: 'notifications/initialized' }) })
  if (!initialized.ok) throw new Error(`MCP initialization notification failed (HTTP ${initialized.status})`)
  const { tools } = await rpc(2, 'tools/list', {})
  const names = new Set(tools?.map(tool => tool.name))
  for (const required of ['list_peers', 'message_peer', 'check_inbox', 'message_receipt']) {
    if (!names.has(required)) throw new Error(`Missing required Bus tool: ${required}`)
  }
  if (!legacy) for (const required of ['create_channel', 'post_channel', 'read_channel', 'upload_attachment', 'read_attachment']) {
    if (!names.has(required)) throw new Error(`Missing workspace tool: ${required}`)
  }
  console.log(`Verified ${url.origin}/mcp: HTTPS, readiness, key authentication, hidden admin routes, and ${names.size} Bus tools.`)
  console.log('This check does not certify a named app, native wake, real account sign-in, message delivery, payments, or backups.')
}

main().catch(error => { console.error(error.message); process.exitCode = 1 })
