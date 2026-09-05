import type { EmitterWebhookEvent } from '@octokit/webhooks'

type PullRequestPayload = EmitterWebhookEvent<'pull_request'>['payload']

export interface ReviewPolicy {
  repositories: string[]
  excludedRepositories: string[]
  autoReview: boolean
  reviewOnRequest: boolean
  requestedReviewers: string[]
  requestedTeams: string[]
  ignore: {
    drafts: boolean
    bots: boolean
    authors: string[]
    labels: string[]
    baseBranches: string[]
    headBranches: string[]
  }
}

// Patterns support *. Empty repositories disables reviews; ['*'] allows all installed repos.
export const reviewPolicy: ReviewPolicy = {
  repositories: ['*'], // e.g. ['my-org/api', 'my-org/*']
  excludedRepositories: [],
  autoReview: true,
  reviewOnRequest: true,
  // Both empty means any requested reviewer/team; otherwise only matching requests trigger.
  requestedReviewers: [], // GitHub user logins, e.g. ['reviewer-login']
  requestedTeams: [], // org/team-slug, e.g. ['my-org/code-review']
  ignore: {
    drafts: true,
    bots: true,
    authors: [], // e.g. ['dependabot[bot]']
    labels: ['skip-review'],
    baseBranches: [], // e.g. ['release/*']; branch patterns are case-sensitive
    headBranches: [], // e.g. ['generated/*']
  },
}

export function getReviewEvent(
  payload: PullRequestPayload,
  policy: ReviewPolicy = reviewPolicy,
): 'updated' | 'closed' | 'ignored' {
  const pr = payload.pull_request
  // Always clean up an existing review, even if its repo or PR is now excluded.
  if (payload.action === 'closed' || pr.state === 'closed' || pr.merged) return 'closed'

  const repo = payload.repository.full_name
  if (!matches(repo, policy.repositories) || matches(repo, policy.excludedRepositories))
    return 'ignored'
  if (policy.ignore.drafts && pr.draft) return 'ignored'
  if (policy.ignore.bots && (pr.user?.type === 'Bot' || pr.user?.login?.endsWith('[bot]')))
    return 'ignored'
  if (matches(pr.user?.login ?? '', policy.ignore.authors)) return 'ignored'
  if (pr.labels.some((label) => matches(label.name ?? '', policy.ignore.labels))) return 'ignored'
  if (matches(pr.base.ref, policy.ignore.baseBranches, true)) return 'ignored'
  if (matches(pr.head.ref, policy.ignore.headBranches, true)) return 'ignored'

  if (payload.action === 'review_requested') {
    if (!policy.reviewOnRequest) return 'ignored'
    if (!policy.requestedReviewers.length && !policy.requestedTeams.length) return 'updated'
    const reviewer = payload.requested_reviewer
    const team = payload.requested_team
    if (reviewer && 'login' in reviewer && matches(reviewer.login, policy.requestedReviewers))
      return 'updated'
    const owner = repo.split('/')[0]
    if (team && matches(`${owner}/${team.slug}`, policy.requestedTeams)) return 'updated'
    return 'ignored'
  }

  // Removing an ignore label can make a PR eligible again. Review-request removal,
  // assignments, and other metadata notifications do not start automatic reviews.
  const automaticUpdate = [
    'opened',
    'reopened',
    'ready_for_review',
    'synchronize',
    'edited',
    'unlabeled',
  ].includes(payload.action)
  return policy.autoReview && automaticUpdate ? 'updated' : 'ignored'
}

function matches(value: string, patterns: string[], caseSensitive = false): boolean {
  return patterns.some((pattern) => {
    const expression = pattern.replace(/[.+?^${}()|[\]\\]/g, '\\$&').replaceAll('*', '.*')
    return new RegExp(`^${expression}$`, caseSensitive ? '' : 'i').test(value)
  })
}
