import assert from 'node:assert/strict'
import { execFileSync } from 'node:child_process'
import { createHash } from 'node:crypto'
import { mkdtempSync, readFileSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join, resolve } from 'node:path'
import { pathToFileURL } from 'node:url'

const sha256 = bytes => createHash('sha256').update(bytes).digest('hex')

// Inspect the bytes from the published archives, not a parallel build or an
// unchecked claim about the checkout. No target binary is executed here.
export function managedRuntimeManifest({ dist, runtimeVersion, sourceCommit, protocolVersion }) {
  assert.match(runtimeVersion, /^\d+\.\d+\.\d+(?:-[0-9A-Za-z.-]+)?(?:\+[0-9A-Za-z.-]+)?$/)
  assert.match(sourceCommit, /^[a-f0-9]{40}$/)
  assert.match(protocolVersion, /^\d+\.\d+$/)
  const temporary = mkdtempSync(join(tmpdir(), 'october-runtime-manifest-'))
  try {
    const artifacts = {}
    for (const arch of ['amd64', 'arm64']) {
      const directory = `agentintercom_${runtimeVersion}_linux_${arch}`
      const archive = `${directory}.tar.gz`
      const archivePath = join(dist, archive)
      const member = `${directory}/agentintercom`
      const entries = execFileSync('tar', ['-tzf', archivePath], { encoding: 'utf8' }).trim().split('\n')
      assert.equal(entries.filter(entry => entry === member).length, 1, `Missing or duplicate helper in ${archive}`)
      const binary = execFileSync('tar', ['-xOzf', archivePath, member], { maxBuffer: 128 * 1024 * 1024 })
      const binaryPath = join(temporary, `agentintercom-${arch}`)
      writeFileSync(binaryPath, binary)
      const info = JSON.parse(execFileSync('go', ['version', '-m', '-json', binaryPath], { encoding: 'utf8' }))
      const settings = Object.fromEntries(info.Settings.map(({ Key, Value }) => [Key, Value]))
      assert.equal(info.Path, 'github.com/october-dev/agent-intercom/cmd/agentintercom', 'Wrong helper program')
      for (const [key, expected] of Object.entries({ GOOS: 'linux', GOARCH: arch, CGO_ENABLED: '0', 'vcs.revision': sourceCommit, 'vcs.modified': 'false' })) {
        assert.equal(settings[key], expected, `${archive}: unexpected ${key}`)
      }
      artifacts[arch] = { archive, sha256: sha256(readFileSync(archivePath)), binarySha256: sha256(binary) }
    }
    return { version: 1, runtimeVersion, protocolVersion, sourceCommit, sourceModifiedAtBuild: false, qualification: 'release', artifacts }
  } finally {
    rmSync(temporary, { recursive: true, force: true })
  }
}

if (process.argv[1] && import.meta.url === pathToFileURL(resolve(process.argv[1])).href) {
  const [dist, runtimeVersion, sourceCommit, protocolVersion, ...extra] = process.argv.slice(2)
  assert.ok(dist && extra.length === 0, 'Usage: managed-runtime-manifest.mjs <dist> <runtime-version> <source-commit> <protocol-version>')
  const manifest = managedRuntimeManifest({ dist, runtimeVersion, sourceCommit, protocolVersion })
  writeFileSync(join(dist, 'remote-bus-runtime.json'), JSON.stringify(manifest, null, 2) + '\n')
}
