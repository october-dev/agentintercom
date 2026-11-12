import test from 'node:test'
import assert from 'node:assert/strict'
import { approvedForRelease, ownerAuthorizedRelease } from './release-policy.mjs'

const pr = { merged_at: '2026-09-05', base: { ref: 'main' }, merge_commit_sha: 'release', head: { sha: 'head' }, user: { login: 'author' } }
const review = { id: 1, state: 'APPROVED', commit_id: 'head', user: { login: 'reviewer', type: 'User' }, author_association: 'COLLABORATOR', submitted_at: '2026-09-04T12:00:00Z' }
test('requires a merged main PR and independent current-head approval', () => {
  assert.equal(approvedForRelease(pr, [review], 'release'), true)
  for (const candidate of [
    { ...pr, merged_at: null },
    { ...pr, base: { ref: 'feature' } },
    { ...pr, merge_commit_sha: 'elsewhere' }
  ]) assert.equal(approvedForRelease(candidate, [review], 'release'), false)
  for (const candidate of [
    { ...review, commit_id: 'old-head' },
    { ...review, user: { login: 'author', type: 'User' } },
    { ...review, user: { login: 'bot', type: 'Bot' } },
    { ...review, author_association: 'CONTRIBUTOR' },
    { ...review, submitted_at: '2026-09-06T00:00:00Z' },
    { ...review, submitted_at: undefined },
    { ...review, state: 'DISMISSED' }
  ]) assert.equal(approvedForRelease(pr, [candidate], 'release'), false)
  assert.equal(approvedForRelease(pr, [], 'release'), false)
})
test('a subsequent dismissal or change request revokes approval; comments do not', () => {
  for (const state of ['DISMISSED', 'CHANGES_REQUESTED']) {
    assert.equal(approvedForRelease(pr, [review, { ...review, id: 2, state }], 'release'), false)
  }
  assert.equal(approvedForRelease(pr, [review, { ...review, id: 2, state: 'COMMENTED' }], 'release'), true)
  assert.equal(approvedForRelease(pr, [review, { ...review, id: 2, state: 'PENDING' }], 'release'), true)
  assert.equal(approvedForRelease(pr, [review, { ...review, id: 2, state: 'CHANGES_REQUESTED', user: { login: 'another', type: 'User' } }], 'release'), false)
})

test('owner authorization requires an explicit release action and a live human admin identity', () => {
  const authorization = { actor: 'owner', eventName: 'workflow_dispatch', ref: 'refs/heads/main', permission: { permission: 'admin', user: { login: 'owner', type: 'User' } } }
  assert.equal(ownerAuthorizedRelease(authorization), true)
  assert.equal(ownerAuthorizedRelease({ ...authorization, eventName: 'push', ref: 'refs/tags/v0.1.0-rc.5' }), true)
  for (const change of [
    { actor: 'someone-else' },
    { eventName: 'pull_request' },
    { eventName: 'push' },
    { ref: 'refs/heads/feature' },
    { eventName: 'push', ref: 'refs/tags/not-a-release' },
    { permission: { permission: 'write', user: { login: 'owner', type: 'User' } } },
    { permission: { permission: 'admin', user: { login: 'owner', type: 'Bot' } } },
    { permission: undefined }
  ]) assert.equal(ownerAuthorizedRelease({ ...authorization, ...change }), false)
})

test('owner approval permits a self-authored release but never bypasses merged-main or unresolved changes', () => {
  const authorization = { ownerApproved: true }
  assert.equal(approvedForRelease(pr, [], 'release', authorization), true)
  assert.equal(approvedForRelease(pr, [{ ...review, user: pr.user }], 'release', authorization), true)
  for (const candidate of [
    { ...pr, merged_at: null },
    { ...pr, base: { ref: 'feature' } },
    { ...pr, merge_commit_sha: 'elsewhere' }
  ]) assert.equal(approvedForRelease(candidate, [], 'release', authorization), false)
  assert.equal(approvedForRelease(pr, [{ ...review, state: 'CHANGES_REQUESTED' }], 'release', authorization), false)
})
