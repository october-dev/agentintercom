import { execFileSync } from 'node:child_process'
import { fileURLToPath } from 'node:url'

export function ownerAuthorizedRelease({ actor, eventName, ref, permission }) {
  const explicitRelease = (eventName === 'workflow_dispatch' && ref === 'refs/heads/main') ||
    (eventName === 'push' && /^refs\/tags\/v[^/]+$/.test(ref ?? ''))
  return explicitRelease && typeof actor === 'string' && actor.length > 0 &&
    permission?.permission === 'admin' && permission.user?.type === 'User' &&
    permission.user.login?.toLowerCase() === actor.toLowerCase()
}

export function approvedForRelease(pr, reviews, sha, { ownerApproved = false } = {}) {
  if (!pr.merged_at || pr.base?.ref !== 'main' || pr.merge_commit_sha !== sha) return false
  const trusted = review => review.user?.type === 'User' && typeof review.user.login === 'string' &&
    ['OWNER', 'MEMBER', 'COLLABORATOR'].includes(review.author_association)
  const latest = new Map()
  for (const review of [...reviews].sort((a, b) => a.id - b.id)) {
    // Comments and unpublished drafts do not dismiss a submitted review.
    if (trusted(review) && ['APPROVED', 'CHANGES_REQUESTED', 'DISMISSED'].includes(review.state)) {
      latest.set(review.user.login.toLowerCase(), review)
    }
  }
  // One maintainer approval must not silently override another maintainer's
  // unresolved changes request, including one submitted after the merge.
  if ([...latest.values()].some(review => review.state === 'CHANGES_REQUESTED')) return false
  return ownerApproved || [...latest.values()].some((review) =>
    review.state === 'APPROVED' &&
    review.commit_id === pr.head?.sha &&
    review.user.login.toLowerCase() !== pr.user?.login?.toLowerCase() &&
    Number.isFinite(Date.parse(review.submitted_at)) &&
    Date.parse(review.submitted_at) <= Date.parse(pr.merged_at)
  )
}

function verifyRelease() {
  const repository = process.env.GITHUB_REPOSITORY
  const sha = process.env.GITHUB_SHA
  if (!/^[\w.-]+\/[\w.-]+$/.test(repository ?? '') || !/^[a-f0-9]{40}$/.test(sha ?? '')) {
    throw new Error('Expected repository and full commit SHA from GitHub Actions')
  }
  execFileSync('git', ['fetch', 'origin', 'main'], { stdio: 'inherit' })
  execFileSync('git', ['merge-base', '--is-ancestor', sha, 'origin/main'], { stdio: 'inherit' })
  const get = (path) => JSON.parse(execFileSync('gh', ['api', path], { encoding: 'utf8' }))
  const pulls = get(`repos/${repository}/commits/${sha}/pulls?per_page=100`)
  let ownerApproved
  for (const candidate of pulls) {
    const pr = get(`repos/${repository}/pulls/${candidate.number}`)
    if (pr.merge_commit_sha !== sha) continue
    const pages = JSON.parse(execFileSync('gh', ['api', '--paginate', '--slurp', `repos/${repository}/pulls/${candidate.number}/reviews?per_page=100`], { encoding: 'utf8' }))
    if (approvedForRelease(pr, pages.flat(), sha)) return
    if (ownerApproved === undefined) {
      const actor = process.env.GITHUB_TRIGGERING_ACTOR || process.env.GITHUB_ACTOR
      const permission = /^[\w-]+$/.test(actor ?? '')
        ? get(`repos/${repository}/collaborators/${actor}/permission`)
        : undefined
      ownerApproved = ownerAuthorizedRelease({ actor, permission, eventName: process.env.GITHUB_EVENT_NAME, ref: process.env.GITHUB_REF })
    }
    if (approvedForRelease(pr, pages.flat(), sha, { ownerApproved })) {
      console.log(`Release authorized by repository administrator ${process.env.GITHUB_TRIGGERING_ACTOR || process.env.GITHUB_ACTOR}`)
      return
    }
  }
  throw new Error('Release requires a merged main PR and either independent final-head approval before merge or an explicit release action by a human repository administrator, with no unresolved trusted change requests')
}

if (process.argv[1] === fileURLToPath(import.meta.url)) verifyRelease()
