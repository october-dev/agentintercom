import assert from 'node:assert/strict'
import test from 'node:test'
import { releaseVersion, validateRunFile } from './rehearse-upgrade.mjs'

test('rehearsal cannot send credentials outside its own child loopback endpoint', () => {
  const run = { address: 'http://127.0.0.1:1234', pid: 42, adminToken: 'a'.repeat(43) }
  assert.equal(validateRunFile(run, 42), run)
  for (const address of ['http://example.com:1234', 'http://127.0.0.1:1234/path', 'http://user:secret@127.0.0.1:1234', 'http://127.0.0.1:1234?token=secret']) {
    assert.throws(() => validateRunFile({ ...run, address }, 42))
  }
  assert.throws(() => validateRunFile(run, 99))
})

test('rehearsal accepts only released baselines', () => {
  assert.equal(releaseVersion('agentintercom 0.2.0 (protocol 0.1)'), '0.2.0')
  assert.equal(releaseVersion('october-bus 0.1.0-rc.4 (protocol 0.1)'), '0.1.0-rc.4')
  for (const output of ['agentintercom dev (protocol 0.1)', 'agentintercom 0.2.0 (protocol 0.2)', 'other 0.2.0 (protocol 0.1)']) {
    assert.throws(() => releaseVersion(output))
  }
})
