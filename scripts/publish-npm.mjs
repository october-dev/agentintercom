import assert from 'node:assert/strict'
import { fileURLToPath } from 'node:url'
import { setTimeout } from 'node:timers/promises'
import { manifest, npm, packageFor, readArtifact, root, targets, validateDistribution } from './npm-distribution.mjs'
import { sourceIdentity } from './artifact-integrity.mjs'

const registry = ['--registry', 'https://registry.npmjs.org/']
export function validateChannel(version, channel) {
  const stable = /^(0|[1-9]\d*)\.(0|[1-9]\d*)\.(0|[1-9]\d*)$/.test(version)
  const identifier = '(?:0|[1-9][0-9]*|[0-9A-Za-z-]*[A-Za-z-][0-9A-Za-z-]*)'
  const prerelease = new RegExp(`^(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)\\.(0|[1-9][0-9]*)-${identifier}(?:\\.${identifier})*$`).test(version)
  assert.ok(['next', 'candidate', 'latest'].includes(channel), 'Expected next, candidate, or latest channel')
  assert.ok(channel === 'next' ? prerelease : stable, `${channel} requires a ${channel === 'next' ? 'prerelease' : 'stable'} version`)
}

function publishedIntegrity(pkg, runNpm, version) {
  let published
  try {
    published = JSON.parse(runNpm(['view', `${pkg.name}@${version}`, 'dist.integrity', '--json', '--prefer-online', ...registry], { stdio: ['ignore', 'pipe', 'pipe'] }))
  } catch (error) {
    let code
    try { code = JSON.parse(error.stdout).error?.code } catch { /* network/auth errors must not be ignored */ }
    if (code !== 'E404') throw error
    return undefined
  }
  assert.ok(typeof published === 'string' && published.length > 0, 'Registry returned invalid artifact integrity; no writes permitted')
  return published
}

export async function publishDistribution(packages, runNpm = npm, { version = manifest.version, channel = 'next', wait = setTimeout } = {}) {
  validateChannel(version, channel)
  assert.deepEqual(packages.map(pkg => pkg.name), [...targets.map(target => packageFor(target).name), manifest.name], 'Expected exactly six native packages followed by the parent')
  // Preflight every immutable version before the first write. A conflict or
  // registry outage on the last package must not partially publish the first six.
  const existing = new Set()
  for (const pkg of packages) {
    const published = publishedIntegrity(pkg, runNpm, version)
    if (published !== undefined) {
      assert.equal(published, pkg.integrity, `Refusing to reuse ${pkg.name}@${version} with different contents`)
      existing.add(pkg.name)
    }
  }
  if (channel === 'latest') {
    assert.equal(existing.size, packages.length, 'Promotion requires all seven identical artifacts already published; publish candidate first')
    // Complete all preflights before changing any tag; parent is promoted last.
    // Tags are not transactional. A retry repeats safe exact-version assignments.
    for (const pkg of packages) runNpm(['dist-tag', 'add', `${pkg.name}@${version}`, 'latest', ...registry], { stdio: 'inherit' })
    return
  }
  for (const pkg of packages) {
    if (existing.has(pkg.name)) {
      console.log(`Already published identical artifact: ${pkg.name}@${version}`)
    } else {
      runNpm(['publish', pkg.file, '--ignore-scripts', '--provenance', '--access', 'public', '--tag', channel, ...registry], { stdio: 'inherit' })
      // npm can accept a publication before its public version listing exists.
      // Retry only missing reads; never repeat the accepted publish operation.
      for (let attempt = 0; ; attempt++) {
        const published = publishedIntegrity(pkg, runNpm, version)
        if (published !== undefined) {
          assert.equal(published, pkg.integrity, `Published integrity mismatch: ${pkg.name}@${version}`)
          break
        }
        assert.ok(attempt < 59, `Timed out waiting for npm to expose ${pkg.name}@${version}; rerun with the original artifacts after registry propagation`)
        if (attempt === 0) console.log(`Waiting for npm to expose ${pkg.name}@${version}`)
        await wait(10_000)
      }
    }
  }
}

if (process.argv[1] === fileURLToPath(import.meta.url)) {
  validateDistribution()
  const channel = process.env.OCTOBER_BUS_NPM_CHANNEL ?? 'next'
  validateChannel(manifest.version, channel)
  if (channel !== 'next') {
    assert.equal(process.env.OCTOBER_BUS_CONFIRM_VERSION, manifest.version, 'Stable publication/promotion requires explicit confirmation of the exact package version')
  }
  // Validate all seven checked artifacts and their source before registry writes.
  // Promotion consumes the original candidate's tarballs and clean checkout.
  // Rebuilding after evidence commits would change the immutable artifacts.
  if (channel === 'latest') assert.ok(process.env.OCTOBER_BUS_CANDIDATE_SOURCE, 'Promotion requires the original candidate source checkout and artifacts')
  const source = sourceIdentity(channel === 'latest' ? process.env.OCTOBER_BUS_CANDIDATE_SOURCE : root)
  const packages = [...targets.map(target => packageFor(target).name), manifest.name].map(name => readArtifact(name, source))
  if (channel === 'latest') {
    // This gate deliberately cannot turn missing real-harness evidence into a
    // passing mock result. Actual accounts, public logs and review remain required.
    const { execFileSync } = await import('node:child_process')
    execFileSync(process.execPath, ['scripts/check-compatibility.mjs', '--runtime', manifest.version, '--source-commit', source.commit, '--require-attestation', '--launch-core'], { cwd: root, stdio: 'inherit' })
  }
  await publishDistribution(packages, npm, { channel })
}
