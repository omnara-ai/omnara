import type { EmitterWebhookEvent } from '@octokit/webhooks'

export const reviewInstructions = `You are an automated pull request reviewer. Each conversation is one pull request; the
first message describes it and later messages describe new pushes.

You have a Linux machine with GITHUB_TOKEN (read-only, scoped to the repository) and
comment.sh already present. Follow the setup commands in the first message to clone the PR
branch. Never print GITHUB_TOKEN or REVIEW_TOKEN.

Review the change as a careful senior engineer: correctness, edge cases, security, tests.
Ignore formatting and style unless it hides a bug. Prefer a few high-signal findings over
many small ones. When a finding is on a specific line, post it inline. Always finish a
review pass with exactly one review verdict via comment.sh, even when there is nothing to
report (use COMMENT with a one-line summary). If the tests are quick to run, run them and
say what happened.

You can only communicate with the author through comment.sh; text you write in this
conversation is not visible on GitHub.`

export function createReviewMessage(
  payload: EmitterWebhookEvent<'pull_request'>['payload'],
): string {
  const pr = payload.pull_request
  return `Review pull request #${pr.number} in ${payload.repository.full_name}: "${pr.title}" by @${pr.user?.login ?? 'unknown'}.

PR: ${pr.html_url}
Base: ${pr.base.ref} (${pr.base.sha})
Head: ${pr.head.ref} (${pr.head.sha}) from ${pr.head.repo?.full_name ?? 'a fork'}

Description:
${pr.body?.trim() || '(no description)'}

Your machine has GITHUB_TOKEN (read-only, scoped to this repository), GITHUB_REPOSITORY, PR_NUMBER,
PR_BASE_REF, and PR_HEAD_REF in its environment, and comment.sh on PATH.

Set up:
  git clone --branch "$PR_HEAD_REF" "https://x-access-token:$GITHUB_TOKEN@github.com/$GITHUB_REPOSITORY.git" repo
  cd repo && git fetch origin "$PR_BASE_REF"

Review:
- Read the diff (git diff origin/$PR_BASE_REF...HEAD) and enough surrounding code to judge it.
- Run the project's tests or build if it is quick and obvious how; report the result, do not fix it.
- Look for correctness bugs, unhandled edge cases, security issues, and missing tests. Skip style nits.

Report back through GitHub using comment.sh (run "comment.sh help" for usage):
- Inline findings: comment.sh line <path> <line> <body>
- Verdict: comment.sh review APPROVE|REQUEST_CHANGES|COMMENT <summary>
- A pre-existing bug unrelated to this PR: comment.sh issue <title> <body>
Post exactly one review verdict per push. Keep comments specific, cite file and line, and never post the same finding twice.`
}

export function createUpdateMessage(
  payload: EmitterWebhookEvent<'pull_request'>['payload'],
): string {
  const pr = payload.pull_request
  const changes =
    payload.action === 'synchronize'
      ? `Previous head: ${payload.before}
New head:      ${payload.after}
Compare:       ${payload.repository.html_url}/compare/${payload.before}...${payload.after}

Review only what changed since your last review (git diff ${payload.before}..${payload.after}).`
      : `Revisit your review using the current title, description, base, and head below. Account for changes to the review request even if no new commits were pushed.`

  return `Pull request #${pr.number} in ${payload.repository.full_name} received a ${payload.action} event from @${payload.sender?.login ?? 'unknown'}.

PR: ${pr.html_url}
Title: ${pr.title}
Base: ${pr.base.ref} (${pr.base.sha})
Head: ${pr.head.ref} (${pr.head.sha})

Description:
${pr.body?.trim() || '(no description)'}

${changes}

In your existing clone run: git fetch origin ${pr.head.sha} && git checkout -q ${pr.head.sha}
Fetch the current base with: git fetch origin ${pr.base.sha}
GITHUB_TOKEN was refreshed for this event; new processes see the new value.

Check whether your earlier findings were addressed and post one review verdict with comment.sh. Do not repeat findings that still stand unchanged; mention them once in the summary instead.`
}
