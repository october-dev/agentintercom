import assert from 'node:assert/strict'
import { EventEmitter } from 'node:events'
import { chmodSync, mkdirSync, mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'

const require = createRequire(import.meta.url)
const { targets, platformPackage, resolveBinary, run } = require('../cli/agentintercom.cjs')
const { version } = require('../package.json')

test('each supported platform resolves an exact-version optional binary', t => {
  const root = mkdtempSync(join(tmpdir(), 'october-cli-'))
  t.after(() => rmSync(root, { recursive: true, force: true }))
  for (const target of targets) {
    const [os, cpu] = target.split('-')
    const { name, binary } = platformPackage(os, cpu)
    const directory = join(root, target)
    mkdirSync(join(directory, 'bin'), { recursive: true })
    const metadata = join(directory, 'package.json')
    const executable = join(directory, 'bin', binary)
    writeFileSync(metadata, JSON.stringify({ name, version }))
    writeFileSync(executable, 'fixture')
    chmodSync(executable, 0o755)
    const resolve = request => { assert.equal(request, `${name}/package.json`); return metadata }
    assert.equal(resolveBinary(os, cpu, resolve), executable)
    writeFileSync(metadata, JSON.stringify({ name, version: '0.0.0' }))
    assert.throws(() => resolveBinary(os, cpu, resolve), /package mismatch/)
    writeFileSync(metadata, JSON.stringify({ name, version }))
    rmSync(executable)
    assert.throws(() => resolveBinary(os, cpu, resolve), /missing or not executable/)
  }
})

test('unsupported systems and missing optional packages fail with actionable instructions', () => {
  assert.throws(() => platformPackage('freebsd', 'x64'), /build the Go daemon/)
  assert.throws(() => platformPackage('linux', 'arm'), /No Agent Intercom npm binary/)
  assert.throws(() => resolveBinary('linux', 'x64', () => { throw new Error('missing') }), /--include=optional/)
})

test('launcher forwards literal arguments, stdio, signals, and exit status without a shell', () => {
  for (const outcome of [0, 17, 'SIGTERM']) {
    const child = new EventEmitter()
    const host = new EventEmitter()
    host.pid = 42
    const forwarded = []
    child.kill = signal => forwarded.push(signal)
    host.kill = (pid, signal) => { assert.equal(pid, 42); assert.equal(signal, outcome) }
    const args = ['scope', 'create', 'spaces; $(literal)']
    run(args, '/fixture/native', (file, actual, options) => {
      assert.equal(file, '/fixture/native')
      assert.deepEqual(actual, args)
      assert.deepEqual(options, { stdio: 'inherit', shell: false })
      return child
    }, host)
    host.emit('SIGINT')
    host.emit('SIGTERM')
    assert.deepEqual(forwarded, ['SIGINT', 'SIGTERM'])
    child.emit('exit', typeof outcome === 'number' ? outcome : null, typeof outcome === 'string' ? outcome : null)
    if (typeof outcome === 'number') assert.equal(host.exitCode, outcome)
    assert.equal(host.listenerCount('SIGINT') + host.listenerCount('SIGTERM'), 0)
  }
})

test('spawn failure reports failure and removes signal handlers', t => {
  t.mock.method(console, 'error', () => {})
  const child = new EventEmitter()
  const host = new EventEmitter()
  run([], '/missing', () => child, host)
  child.emit('error', new Error('ENOENT'))
  assert.equal(host.exitCode, 1)
  assert.equal(host.listenerCount('SIGINT') + host.listenerCount('SIGTERM'), 0)
})
