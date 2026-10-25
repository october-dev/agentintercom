import assert from 'node:assert/strict'
import { mkdtempSync, rmSync, writeFileSync } from 'node:fs'
import { tmpdir } from 'node:os'
import { join } from 'node:path'
import test from 'node:test'
import { integrity, validateArtifact } from './artifact-integrity.mjs'

test('artifact checks reject stale source, wrong package metadata and altered bytes', t => {
  const directory = mkdtempSync(join(tmpdir(), 'october-artifact-test-'))
  t.after(() => rmSync(directory, { recursive: true }))
  const file = join(directory, 'package.tgz')
  writeFileSync(file, 'synthetic tarball')
  const expected = { name: 'package', version: '1.0.0-next.1', requiredPath: 'bin/agentintercom', source: { commit: 'a', sourceDigest: 'b' } }
  const record = { ...expected, schemaVersion: 1, integrity: integrity(file) }
  assert.equal(validateArtifact(record, expected, file).name, 'package')
  for (const changed of [{ name: 'other' }, { version: 'old' }, { requiredPath: 'other' }, { source: { commit: 'old' } }, { schemaVersion: 2 }]) {
    assert.throws(() => validateArtifact({ ...record, ...changed }, expected, file))
  }
  writeFileSync(file, 'changed after packing')
  assert.throws(() => validateArtifact(record, expected, file), /contents differ/)
})
