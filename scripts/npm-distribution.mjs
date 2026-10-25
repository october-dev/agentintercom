import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { chmodSync, copyFileSync, cpSync, mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { createRequire } from 'node:module'
import { dirname, join, resolve } from 'node:path'
import { fileURLToPath } from 'node:url'
import { integrity, sourceIdentity, validateArtifact } from './artifact-integrity.mjs'

const require = createRequire(import.meta.url)
export const root = resolve(dirname(fileURLToPath(import.meta.url)), '..')
export const sdk = join(root, 'sdk/typescript')
export const artifacts = join(root, 'dist/npm')
export const manifest = JSON.parse(readFileSync(join(sdk, 'package.json'), 'utf8'))
export const { targets, platformPackage } = require('../sdk/typescript/cli/agentintercom.cjs')

export function npm(args, options = {}) {
  // Invoked through npm run: this also avoids a shell or npm.cmd on Windows.
  assert.ok(process.env.npm_execpath, 'Run this script through the package.json npm scripts')
  return execFileSync(process.execPath, [process.env.npm_execpath, ...args], { encoding: 'utf8', ...options })
}

export function packageFor(target) {
  assert.ok(targets.includes(target), `Unsupported npm target: ${target}`)
  const [os, cpu] = target.split('-')
  const platform = platformPackage(os, cpu)
  return { ...platform, os, cpu, goos: os === 'win32' ? 'windows' : os, goarch: cpu === 'x64' ? 'amd64' : 'arm64' }
}

export function tarball(name) {
  return join(artifacts, `${name.replace('@', '').replace('/', '-')}-${manifest.version}.tgz`)
}

export function validateDistribution() {
  const part = '(?:0|[1-9][0-9]*)'
  const pre = '(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)'
  assert.match(manifest.version, new RegExp(`^${part}\\.${part}\\.${part}(?:-${pre}(?:\\.${pre})*)?$`))
  assert.equal(manifest.bin['agentintercom'], 'cli/agentintercom.cjs')
  const lock = JSON.parse(readFileSync(join(sdk, 'package-lock.json'), 'utf8'))
  assert.equal(lock.version, manifest.version, 'Update the source lockfile version')
  assert.equal(lock.packages[''].version, manifest.version, 'Update the root lockfile package version')
}

export function distributionManifest() {
  const { scripts, devDependencies, ...published } = manifest
  return { ...published,
    files: [...published.files, 'extensions/pi/*.mjs', 'extensions/pi/README.md'],
    pi: { extensions: ['./extensions/pi/index.mjs'] },
    optionalDependencies: Object.fromEntries(targets.map(target => [packageFor(target).name, manifest.version])) }
}

export function requiredPath(name) {
  if (name === manifest.name) return manifest.bin['agentintercom']
  const platform = targets.map(packageFor).find(platform => platform.name === name)
  assert.ok(platform, `Unexpected distribution package: ${name}`)
  return `bin/${platform.binary}`
}

export function readArtifact(name, source = sourceIdentity(root)) {
  return validateArtifact(JSON.parse(readFileSync(`${tarball(name)}.json`, 'utf8')), {
    name, version: manifest.version, requiredPath: requiredPath(name), source
  }, tarball(name))
}

function build(target, source) {
  const platform = packageFor(target)
  const directory = join(artifacts, target)
  mkdirSync(join(directory, 'bin'), { recursive: true })
  const binary = join(directory, 'bin', platform.binary)
  execFileSync('go', ['build', '-trimpath', '-ldflags', `-s -w -X github.com/october-dev/october-bus/bus.Version=${manifest.version}`, '-o', binary, './cmd/agentintercom'], {
    cwd: root, stdio: 'inherit', env: { ...process.env, CGO_ENABLED: '0', GOOS: platform.goos, GOARCH: platform.goarch }
  })
  chmodSync(binary, 0o755)
  const pkg = {
    name: platform.name, version: manifest.version, description: `Agent Intercom native Go binary for ${target}.`,
    license: manifest.license, os: [platform.os], cpu: [platform.cpu],
    repository: manifest.repository, homepage: manifest.homepage,
    files: ['bin', 'LICENSE', 'README.md'], publishConfig: manifest.publishConfig,
    agentIntercomBuild: { source, binaryIntegrity: integrity(binary) }
  }
  writeFileSync(join(directory, 'package.json'), `${JSON.stringify(pkg, null, 2)}\n`)
  copyFileSync(join(root, 'LICENSE'), join(directory, 'LICENSE'))
  writeFileSync(join(directory, 'README.md'), `# ${platform.name}\n\nNative Go executable for Agent Intercom on ${target}. Installed automatically by \`${manifest.name}@${manifest.version}\`; use that package rather than installing this internal platform package directly.\n`)
  console.log(`Built ${platform.name}@${manifest.version}`)
}

function pack(directory, name, source) {
  const pkg = JSON.parse(readFileSync(join(directory, 'package.json'), 'utf8'))
  assert.equal(pkg.name, name)
  assert.equal(pkg.version, manifest.version, 'Stale native package version; rebuild first')
  const expectedBinary = requiredPath(name)
  if (name === manifest.name) {
    assert.deepEqual(pkg.optionalDependencies, distributionManifest().optionalDependencies)
    assert.deepEqual(pkg.bin, manifest.bin)
    assert.equal(pkg.scripts, undefined, 'Published packages must not run lifecycle scripts')
  } else {
    const platform = targets.map(packageFor).find(platform => platform.name === name)
    assert.deepEqual(pkg.os, [platform.os])
    assert.deepEqual(pkg.cpu, [platform.cpu])
    assert.deepEqual(pkg.agentIntercomBuild, { source, binaryIntegrity: integrity(join(directory, expectedBinary)) }, 'Stale or changed native binary; rebuild before packing')
  }
  const [result] = JSON.parse(npm(['pack', '--json', '--ignore-scripts', '--pack-destination', artifacts], { cwd: directory }))
  assert.equal(result.name, name)
  assert.equal(result.version, manifest.version)
  assert.ok(result.files.some(file => file.path === expectedBinary), `Packed binary/launcher missing: ${expectedBinary}`)
  if (name === manifest.name) {
    for (const file of ['extensions/pi/index.mjs', 'extensions/pi/bridge.mjs']) assert.ok(result.files.some(entry => entry.path === file), `Packed Pi extension missing: ${file}`)
  }
  const record = { schemaVersion: 1, name, version: manifest.version, requiredPath: expectedBinary, source, integrity: integrity(tarball(name)) }
  assert.equal(result.integrity, record.integrity)
  writeFileSync(`${tarball(name)}.json`, `${JSON.stringify(record, null, 2)}\n`)
  console.log(`Packed ${result.filename}`)
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  validateDistribution()
  const [command, selection = `${process.platform}-${process.arch}`, ...extra] = process.argv.slice(2)
  assert.ok(['build', 'pack'].includes(command) && extra.length === 0, 'Expected build|pack [--all|os-arch]')
  const selected = selection === '--all' ? targets : [packageFor(selection).target]
  const source = sourceIdentity(root)
  mkdirSync(artifacts, { recursive: true })
  for (const target of selected) {
    if (command === 'build') build(target, source)
    else pack(join(artifacts, target), packageFor(target).name, source)
  }
  if (command === 'pack') {
    // Generate release-only dependencies here. Source npm ci must also work
    // before these exact-version platform packages have been published.
    const stagedSDK = mkdtempSync(join(artifacts, 'sdk-'))
    try {
      npm(['run', 'build', '--', '--outDir', join(stagedSDK, 'dist'), '--sourceRoot', '../src'], { cwd: sdk, stdio: 'inherit' })
      for (const path of ['cli', 'src', 'LICENSE', 'README.md']) {
        cpSync(join(sdk, path), join(stagedSDK, path), { recursive: true })
      }
      mkdirSync(join(stagedSDK, 'extensions/pi'), { recursive: true })
      for (const file of ['index.mjs', 'bridge.mjs']) copyFileSync(join(root, 'adapters/pi', file), join(stagedSDK, 'extensions/pi', file))
      writeFileSync(join(stagedSDK, 'extensions/pi/README.md'), `# Pi extension\n\nExperimental native session adapter. Generate configuration with agentintercom harness config pi and set OCTOBER_BUS_PI_CONFIG to its reviewed absolute path. No host certification is implied.\n\n[Setup and limitations](https://github.com/october-dev/october-bus/blob/${source.commit}/adapters/pi/README.md)\n`)
      writeFileSync(join(stagedSDK, 'package.json'), `${JSON.stringify(distributionManifest(), null, 2)}\n`)
      pack(stagedSDK, manifest.name, source)
    } finally {
      rmSync(stagedSDK, { recursive: true })
    }
  }
  assert.deepEqual(sourceIdentity(root), source, 'Source changed during build/pack; repeat before publishing')
}
