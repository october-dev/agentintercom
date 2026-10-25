import assert from 'node:assert/strict'
import test from 'node:test'
import { distributionManifest, manifest, packageFor, targets } from './npm-distribution.mjs'
import { publishDistribution, validateChannel } from './publish-npm.mjs'

const packages = [...targets.map(target => packageFor(target).name), manifest.name].map(name => ({ name, file: `${name}.tgz`, integrity: `sha512-${name}` }))
const notFound = () => Object.assign(new Error('missing'), { stdout: JSON.stringify({ error: { code: 'E404' } }) })

test('stable candidate and promotion are explicit, never inferred from a prerelease', () => {
  for (const [version, channel] of [['1.0.0', 'candidate'], ['1.0.0', 'latest'], ['1.0.0-rc.1', 'next']]) validateChannel(version, channel)
  for (const [version, channel] of [['1.0.0', 'next'], ['1.0.0-rc.1', 'latest'], ['1.0.0-rc.1', 'candidate'], ['01.0.0', 'latest'], ['1.0.0', 'other']]) {
    assert.throws(() => validateChannel(version, channel))
  }
})

test('stable promotion preflights all artifacts and promotes parent last; retries are safe', async () => {
  for (const missing of [true, false]) {
    const writes = []
    const run = () => publishDistribution(packages, args => {
      if (args[0] === 'view') {
        const pkg = packages.find(pkg => args[1] === `${pkg.name}@1.0.0`)
        if (missing && pkg.name === manifest.name) throw notFound()
        return JSON.stringify(pkg.integrity)
      }
      assert.deepEqual(args.slice(0, 2), ['dist-tag', 'add'])
      assert.equal(args[3], 'latest')
      writes.push(args[2])
      return ''
    }, { version: '1.0.0', channel: 'latest' })
    if (missing) {
      await assert.rejects(run, /publish candidate first/)
      assert.deepEqual(writes, [])
    } else {
      await run()
      await run()
      assert.deepEqual(writes, [...packages, ...packages].map(pkg => `${pkg.name}@1.0.0`))
    }
  }
})

test('stable candidate publishes without assigning latest', async () => {
  const present = new Set()
  await publishDistribution(packages, args => {
    if (args[0] === 'view') {
      const pkg = packages.find(pkg => args[1] === `${pkg.name}@1.0.0`)
      if (!present.has(pkg.name)) throw notFound()
      return JSON.stringify(pkg.integrity)
    }
    assert.equal(args[0], 'publish')
    assert.equal(args[args.indexOf('--tag') + 1], 'candidate')
    present.add(packages.find(pkg => pkg.file === args[1]).name)
    return ''
  }, { version: '1.0.0', channel: 'candidate' })
  assert.equal(present.size, 7)
})

test('packed SDK pins every native optional package to the parent version', () => {
  assert.deepEqual(distributionManifest().optionalDependencies, Object.fromEntries(packages.slice(0, -1).map(pkg => [pkg.name, manifest.version])))
  assert.equal(distributionManifest().scripts, undefined)
  assert.equal(distributionManifest().devDependencies, undefined)
})

// Publication fixtures must not depend on the checkout's release version.
const prereleaseVersion = '1.0.0-rc.1'
const publishPrerelease = (packages, run, options = {}) => publishDistribution(packages, run, { version: prereleaseVersion, channel: 'next', ...options })

test('a conflict or outage on the final preflight performs no publishes', async () => {
  for (const failure of ['conflict', 'outage']) {
    await assert.rejects(() => publishPrerelease(packages, args => {
      assert.equal(args[0], 'view', 'preflight must finish before publishing anything')
      if (args[1] !== `${manifest.name}@${prereleaseVersion}`) throw notFound()
      if (failure === 'outage') throw new Error('registry unavailable')
      return JSON.stringify('different')
    }), failure === 'outage' ? /registry unavailable/ : /different contents/)
  }
  await assert.rejects(() => publishPrerelease(packages.slice(1), () => assert.fail('no registry request expected')), /exactly six/)
  for (const value of ['null', '""', '{}']) await assert.rejects(() => publishPrerelease(packages, args => {
    assert.equal(args[0], 'view')
    return value
  }), /invalid artifact integrity/)
})

test('publishes all native packages before the parent, with provenance and exact integrity', async () => {
  const published = []
  await publishPrerelease(packages, args => {
    const pkg = packages.find(pkg => args[1] === pkg.file || args[1] === `${pkg.name}@${prereleaseVersion}`)
    assert.ok(pkg)
    if (args[0] === 'view') {
      if (!published.includes(pkg.name)) throw notFound()
      return JSON.stringify(pkg.integrity)
    }
    assert.ok(args.includes('--provenance') && args.includes('--ignore-scripts'))
    assert.equal(args[args.indexOf('--tag') + 1], 'next')
    published.push(pkg.name)
    return ''
  })
  assert.deepEqual(published, packages.map(pkg => pkg.name))
})

test('a native publish failure prevents the parent package from being published', async () => {
  const attempts = []
  await assert.rejects(() => publishPrerelease(packages, args => {
    if (args[0] === 'view') throw notFound()
    attempts.push(args[1])
    throw new Error('no publish permission')
  }), /no publish permission/)
  assert.deepEqual(attempts, [packages[0].file])
})

test('reruns skip identical artifacts but reject different contents and network/auth failures', async t => {
  t.mock.method(console, 'log', () => {})
  await publishPrerelease(packages, args => {
    assert.equal(args[0], 'view')
    return JSON.stringify(packages.find(pkg => args[1] === `${pkg.name}@${prereleaseVersion}`).integrity)
  })
  await assert.rejects(() => publishPrerelease(packages, () => JSON.stringify('different')), /Refusing to reuse/)
  await assert.rejects(() => publishPrerelease(packages, () => { throw new Error('offline') }), /offline/)
})

test('accepted publications wait for visibility without repeating writes or advancing early', async t => {
  t.mock.method(console, 'log', () => {})
  const published = []
  const reads = new Map()
  const waits = []
  await publishPrerelease(packages, args => {
    const pkg = packages.find(pkg => args[1] === pkg.file || args[1] === `${pkg.name}@${prereleaseVersion}`)
    if (args[0] === 'view') {
      assert.ok(args.includes('--prefer-online'))
      if (!published.includes(pkg.name)) throw notFound()
      reads.set(pkg.name, (reads.get(pkg.name) ?? 0) + 1)
      if (reads.get(pkg.name) < 3) throw notFound()
      return JSON.stringify(pkg.integrity)
    }
    assert.equal(args[0], 'publish')
    for (const previous of published) assert.equal(reads.get(previous), 3, 'verify each native package before advancing')
    published.push(pkg.name)
  }, { wait: async delay => waits.push(delay) })
  assert.deepEqual(published, packages.map(pkg => pkg.name), 'publish every artifact exactly once, parent last')
  assert.deepEqual(waits, Array(14).fill(10_000))
})

test('publication visibility timeout is bounded and prevents later package writes', async t => {
  t.mock.method(console, 'log', () => {})
  const writes = []
  let waits = 0
  await assert.rejects(() => publishPrerelease(packages, args => {
    if (args[0] === 'view') throw notFound()
    writes.push(args[1])
  }, { wait: async () => { waits++ } }), /Timed out waiting for npm/)
  assert.deepEqual(writes, [packages[0].file])
  assert.equal(waits, 59)
})

test('post-publication conflicts, malformed metadata and non-404 errors fail without waiting', async () => {
  for (const failure of ['conflict', 'malformed', 'E401', 'E503']) {
    const writes = []
    await assert.rejects(() => publishPrerelease(packages, args => {
      if (args[0] === 'publish') { writes.push(args[1]); return '' }
      if (writes.length === 0) throw notFound()
      if (failure === 'conflict') return JSON.stringify('different')
      if (failure === 'malformed') return '{}'
      throw Object.assign(new Error(failure), { stdout: JSON.stringify({ error: { code: failure } }) })
    }, { wait: async () => assert.fail('Only E404 visibility reads may be retried') }),
    failure === 'conflict' ? /integrity mismatch/ : failure === 'malformed' ? /invalid artifact integrity/ : new RegExp(failure))
    assert.deepEqual(writes, [packages[0].file])
  }
})
