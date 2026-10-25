import assert from 'node:assert/strict'
import { execFile, execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdtempSync, readFileSync, readdirSync, rmSync, writeFileSync } from 'node:fs'
import { createServer } from 'node:http'
import { tmpdir } from 'node:os'
import { dirname, join, resolve } from 'node:path'
import { promisify } from 'node:util'
import { distributionManifest, manifest, packageFor, platformPackage, readArtifact, root as repositoryRoot, tarball, targets, validateDistribution } from './npm-distribution.mjs'

validateDistribution()
assert.ok(process.env.npm_execpath, 'Use npm run test:distribution')
const host = platformPackage()
// Cross-OS release smoke jobs share a commit and build record, but checkout
// line endings may differ. The publisher additionally checks the source digest.
const source = JSON.parse(readFileSync(`${tarball(manifest.name)}.json`, 'utf8')).source
assert.equal(source.commit, execFileSync('git', ['rev-parse', 'HEAD'], { cwd: repositoryRoot, encoding: 'utf8' }).trim())
assert.match(source.sourceDigest, /^sha256:[a-f0-9]{64}$/)
readArtifact(manifest.name, source)
readArtifact(host.name, source)
const root = mkdtempSync(join(tmpdir(), 'october-npm-install-'))
const downloaded = []
const archives = new Map([
  [manifest.name, readFileSync(tarball(manifest.name))],
  [host.name, readFileSync(tarball(host.name))]
])
const packages = new Map([[manifest.name, distributionManifest()], ...targets.map(target => {
  const platform = packageFor(target)
  return [platform.name, { name: platform.name, version: manifest.version, os: [platform.os], cpu: [platform.cpu] }]
})])
let registry
// A local registry exercises optional dependency selection before first publication.
// Other platforms have metadata, but no downloadable binary on this host.
const server = createServer((request, response) => {
  const name = decodeURIComponent(request.url.slice(1))
  if (name.startsWith('tar/')) {
    const key = name.slice(4)
    downloaded.push(key)
    const body = archives.get(key)
    response.writeHead(body ? 200 : 404)
    response.end(body ?? 'wrong platform requested')
    return
  }
  const pkg = packages.get(name)
  if (!pkg) { response.writeHead(404); response.end('{}'); return }
  const body = archives.get(name) ?? archives.get(host.name)
  response.setHeader('Content-Type', 'application/json')
  response.end(JSON.stringify({
    name, 'dist-tags': { latest: manifest.version, next: manifest.version },
    versions: { [manifest.version]: { ...pkg, dist: {
      tarball: `${registry}/tar/${encodeURIComponent(name)}`,
      integrity: `sha512-${createHash('sha512').update(body).digest('base64')}`
    } } }
  }))
})
const execute = promisify(execFile)
const options = { cwd: root, timeout: 90_000, maxBuffer: 4 * 1024 * 1024 }
try {
  await new Promise(resolve => server.listen(0, '127.0.0.1', resolve))
  registry = `http://127.0.0.1:${server.address().port}`
  writeFileSync(join(root, 'package.json'), JSON.stringify({ name: 'october-install-smoke', private: true, version: '1.0.0' }))
  writeFileSync(join(root, 'user.npmrc'), '')
  writeFileSync(join(root, 'global.npmrc'), '')
  const config = ['--registry', registry, '--cache', join(root, 'cache'), '--userconfig', join(root, 'user.npmrc'), '--globalconfig', join(root, 'global.npmrc')]
  const runNpm = args => execute(process.execPath, [process.env.npm_execpath, ...config, ...args], options)
  await runNpm(['install', `${manifest.name}@${manifest.version}`, '--ignore-scripts', '--no-audit', '--no-fund'])
  const installed = JSON.parse(readFileSync(join(root, 'node_modules/agentintercom/package.json'), 'utf8'))
  assert.deepEqual(installed.optionalDependencies, distributionManifest().optionalDependencies)
  assert.equal(installed.scripts, undefined)
  assert.deepEqual(installed.pi, { extensions: ['./extensions/pi/index.mjs'] })
  await execute(process.execPath, ['--input-type=module', '-e', 'const m = await import("./node_modules/agentintercom/extensions/pi/index.mjs"); if (typeof m.default !== "function") process.exit(1)'], options)
  const mapPath = join(root, 'node_modules/agentintercom/dist/client.js.map')
  const sourceMap = JSON.parse(readFileSync(mapPath, 'utf8'))
  assert.equal(resolve(dirname(mapPath), sourceMap.sourceRoot, sourceMap.sources[0]), join(root, 'node_modules/agentintercom/src/client.ts'), 'Source maps must refer to the packaged source, not a temporary build directory')
  assert.deepEqual([...new Set(downloaded)].sort(), [manifest.name, host.name].sort())
  assert.deepEqual(readdirSync(join(root, 'node_modules/@october-dev')).sort(), [`agentintercom-${host.target}`].sort())
  const version = await runNpm(['exec', '--offline', '--', 'agentintercom', 'version'])
  assert.ok(version.stdout.includes(`agentintercom ${manifest.version} `), version.stdout)
  const launcher = join(root, 'node_modules/agentintercom/cli/agentintercom.cjs')
  const demo = await execute(process.execPath, [launcher, 'demo'], options)
  assert.ok(demo.stdout.length > 0)
  await assert.rejects(execute(process.execPath, [launcher, 'not-a-command; literal'], options), error => error.code === 1)
  await execute(process.execPath, ['--input-type=module', '-e', 'import { AgentIntercomClient, AgentIntercomAgentSession } from "agentintercom"; if (typeof AgentIntercomClient !== "function" || typeof AgentIntercomAgentSession.start !== "function") process.exit(1)'], options)
  rmSync(join(root, 'node_modules', host.name), { recursive: true })
  await execute(process.execPath, ['--input-type=module', '-e', 'import { AgentIntercomClient } from "agentintercom"; if (typeof AgentIntercomClient !== "function") process.exit(1)'], options)
  await assert.rejects(execute(process.execPath, [launcher, 'version'], options), error => error.code === 1 && error.stderr.includes('--include=optional'))
  console.log(`Installed ${host.name} automatically; npx-style invocation, demo, SDK-only imports, and failure exit codes passed (${manifest.version}).`)
} finally {
  await new Promise(resolve => server.close(resolve))
  rmSync(root, { recursive: true, force: true })
}
