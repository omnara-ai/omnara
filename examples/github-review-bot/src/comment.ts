import { Octokit } from '@octokit/core'
import { z } from 'zod'
import type { Bindings } from './env'
import { installationToken, type ReviewClaims } from './auth'

export const zReviewCommentRequest = z.discriminatedUnion('kind', [
  z.object({ kind: z.literal('comment'), body: z.string().min(1) }),
  z.object({
    kind: z.literal('line_comment'),
    body: z.string().min(1),
    path: z.string().min(1),
    line: z.number().int().positive(),
    side: z.enum(['LEFT', 'RIGHT']).default('RIGHT'),
  }),
  z.object({
    kind: z.literal('review'),
    event: z.enum(['APPROVE', 'REQUEST_CHANGES', 'COMMENT']),
    body: z.string(),
  }),
  z.object({ kind: z.literal('issue'), title: z.string().min(1), body: z.string() }),
])

export async function postReviewComment(
  env: Bindings,
  claims: ReviewClaims,
  request: z.infer<typeof zReviewCommentRequest>,
) {
  const installation = await installationToken(env, claims, {
    pull_requests: 'write',
    issues: 'write',
    contents: 'read',
  })
  const octokit = new Octokit({ auth: installation.token })
  const [owner, repo] = claims.repo.split('/')

  switch (request.kind) {
    case 'comment': {
      const { data } = await octokit.request(
        'POST /repos/{owner}/{repo}/issues/{issue_number}/comments',
        { owner, repo, issue_number: claims.pr, body: request.body },
      )
      return { id: data.id, url: data.html_url }
    }
    case 'line_comment': {
      const { data: pull } = await octokit.request(
        'GET /repos/{owner}/{repo}/pulls/{pull_number}',
        {
          owner,
          repo,
          pull_number: claims.pr,
        },
      )
      const { data } = await octokit.request(
        'POST /repos/{owner}/{repo}/pulls/{pull_number}/comments',
        {
          owner,
          repo,
          pull_number: claims.pr,
          body: request.body,
          commit_id: pull.head.sha,
          path: request.path,
          line: request.line,
          side: request.side,
        },
      )
      return { id: data.id, url: data.html_url }
    }
    case 'review': {
      const { data } = await octokit.request(
        'POST /repos/{owner}/{repo}/pulls/{pull_number}/reviews',
        {
          owner,
          repo,
          pull_number: claims.pr,
          event: request.event,
          body: request.body,
        },
      )
      return { id: data.id, url: data.html_url }
    }
    case 'issue': {
      const { data } = await octokit.request('POST /repos/{owner}/{repo}/issues', {
        owner,
        repo,
        title: request.title,
        body: `${request.body}\n\n_Opened while reviewing #${claims.pr}._`,
      })
      return { id: data.id, number: data.number, url: data.html_url }
    }
  }
}
