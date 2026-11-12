import assert from 'node:assert/strict'
import test from 'node:test'
import { validateCandidateRun } from './candidate-run.mjs'

test('promotion accepts only successful reviewed-lane main artifacts from the same repository', () => {
  const run = { head_repository: { full_name: 'october-dev/agentintercom' }, head_branch: 'main', path: '.github/workflows/publish-npm-prerelease.yml', event: 'workflow_dispatch', status: 'completed', conclusion: 'success', head_sha: 'a'.repeat(40) }
  assert.equal(validateCandidateRun(run, run.head_repository.full_name), run.head_sha)
  for (const change of [{ head_repository: { full_name: 'outsider/fork' } }, { head_branch: 'feature' }, { path: '.github/workflows/ci.yml' }, { event: 'pull_request' }, { status: 'in_progress' }, { conclusion: 'failure' }, { head_sha: 'main' }]) assert.throws(() => validateCandidateRun({ ...run, ...change }, run.head_repository.full_name))
})
