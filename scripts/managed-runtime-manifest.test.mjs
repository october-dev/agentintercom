import test from 'node:test'
import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdirSync, mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import { managedRuntimeManifest } from './managed-runtime-manifest.mjs'

test('manifest hashes the shipped Linux binaries and rejects dirty, missing or mismatched build provenance', { timeout: 120_000 }, () => {
  const root = mkdtempSync(join(tmpdir(), 'october-manifest-test-'))
  try {
    const command = (bin, args, options = {}) => execFileSync(bin, args, { cwd: root, encoding: 'utf8', ...options })
    command('git', ['init', '-q'])
    command('git', ['config', 'user.email', 'test@example.invalid'])
    command('git', ['config', 'user.name', 'Manifest test'])
    const goVersion = execFileSync('go', ['env', 'GOVERSION'], { encoding: 'utf8' }).trim().replace(/^go/, '')
    writeFileSync(join(root, 'go.mod'), `module github.com/october-dev/october-bus\n\ngo ${goVersion}\n`)
    writeFileSync(join(root, '.gitignore'), 'dist/\n')
    mkdirSync(join(root, 'cmd/agentintercom'), { recursive: true })
    writeFileSync(join(root, 'cmd/agentintercom/main.go'), 'package main\nfunc main() {}\n')
    command('git', ['add', '.'])
    command('git', ['-c', 'commit.gpgsign=false', 'commit', '-qm', 'Fixture'])
    const sourceCommit = command('git', ['rev-parse', 'HEAD']).trim()
    const dist = join(root, 'dist')
    const runtimeVersion = '0.1.0-test.1'
    const build = (arch, goarch = arch, vcs = 'true') => {
      const directory = `agentintercom_${runtimeVersion}_linux_${arch}`
      mkdirSync(join(dist, directory), { recursive: true })
      command('go', ['build', `-buildvcs=${vcs}`, '-o', join(dist, directory, 'agentintercom'), './cmd/agentintercom'], {
        env: { ...process.env, GOOS: 'linux', GOARCH: goarch, CGO_ENABLED: '0' }
      })
      command('tar', ['-czf', join(dist, `${directory}.tar.gz`), '-C', dist, directory])
    }
    build('amd64')
    build('arm64')
    const options = { dist, runtimeVersion, sourceCommit, protocolVersion: '0.1' }
    const manifest = managedRuntimeManifest(options)
    assert.equal(manifest.sourceCommit, sourceCommit)
    assert.equal(manifest.sourceModifiedAtBuild, false)
    assert.equal(manifest.qualification, 'release')
    for (const arch of ['amd64', 'arm64']) {
      const artifact = manifest.artifacts[arch]
      const hash = file => createHash('sha256').update(readFileSync(file)).digest('hex')
      assert.equal(artifact.sha256, hash(join(dist, artifact.archive)))
      assert.equal(artifact.binarySha256, hash(join(dist, `agentintercom_${runtimeVersion}_linux_${arch}`, 'agentintercom')))
    }
    assert.throws(() => managedRuntimeManifest({ ...options, sourceCommit: '0'.repeat(40) }), /vcs.revision/)
    writeFileSync(join(root, 'cmd/agentintercom/main.go'), 'package main\nfunc main() { println("modified") }\n')
    build('arm64')
    assert.throws(() => managedRuntimeManifest(options), /vcs.modified/)
    command('git', ['restore', 'cmd/agentintercom/main.go'])
    build('arm64', 'amd64')
    assert.throws(() => managedRuntimeManifest(options), /GOARCH/)
    build('arm64', 'arm64', 'false')
    assert.throws(() => managedRuntimeManifest(options), /vcs.revision/)
  } finally {
    rmSync(root, { recursive: true, force: true })
  }
})
