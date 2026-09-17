// GitHub's public schema, retrieved 2026-09-15:
// https://docs.github.com/public/fpt/schema.docs.graphql
// SHA-256: 8ecdb21a5c3affdeaa0e55bd9174536aa6c69cbb61f20ec796085aa1509c95df
// These are communication queries only: no repository contents, files or diffs.
const identity = 'id number repository { id }'
const comment = 'id body author { login } createdAt'
const review = `${comment} state commit { oid } submittedAt`
const reviewComment = `${comment} fullDatabaseId state path line originalCommit { oid }
  pullRequestReview { id state } replyTo { id }`

export const githubDocuments = {
  viewer: 'query GitHubViewer { viewer { id login } }',
  commentIdentity: `query GitHubCommentIdentity($comment: ID!) {
    node(id: $comment) { ... on PullRequestReviewComment {
      ${reviewComment} pullRequest { ${identity} }
    } }
  }`,
  pullRequest: `query GitHubPullRequest($repository: ID!, $number: Int!) {
    node(id: $repository) { ... on Repository { id pullRequest(number: $number) {
      ${identity} title state headRefOid
    } } }
  }`,
  pendingReviews: `query GitHubPendingReviews($repository: ID!, $number: Int!, $author: String!) {
    node(id: $repository) { ... on Repository { id pullRequest(number: $number) {
      ${identity}
      reviews(first: 1, states: [PENDING], author: $author) { nodes { id } }
    } } }
  }`,
  timeline: `query GitHubTimeline($repository: ID!, $number: Int!, $limit: Int!, $before: String) {
    node(id: $repository) { ... on Repository { id pullRequest(number: $number) {
      ${identity}
      timelineItems(last: $limit, before: $before, itemTypes: [ISSUE_COMMENT, PULL_REQUEST_REVIEW]) {
        nodes {
          __typename
          ... on IssueComment { ${comment} }
          ... on PullRequestReview { ${review} }
        }
        pageInfo { hasPreviousPage startCursor }
      }
    } } }
  }`,
  thread: `query GitHubReviewThread($thread: ID!, $limit: Int!, $before: String) {
    node(id: $thread) { ... on PullRequestReviewThread {
      id pullRequest { ${identity} }
      comments(last: $limit, before: $before) {
        nodes { ${reviewComment} }
        pageInfo { hasPreviousPage startCursor }
      }
    } }
  }`,
  threadIdentity: `query GitHubThreadIdentity($thread: ID!) {
    node(id: $thread) { ... on PullRequestReviewThread {
      id pullRequest { ${identity} }
      comments(first: 1) { nodes { ${reviewComment} } }
    } }
  }`,
  inboundComment: `query GitHubInboundComment($comment: ID!) {
    node(id: $comment) { ... on PullRequestReviewComment {
      id state replyTo { id } pullRequestReview { id state }
      pullRequest { ${identity} }
    } }
  }`,
  inboundThreads: `query GitHubInboundThreads($repository: ID!, $number: Int!, $after: String) {
    node(id: $repository) { ... on Repository { id pullRequest(number: $number) {
      ${identity}
      reviewThreads(first: 100, after: $after) {
        nodes { id comments(first: 1) { nodes { id state } } }
        pageInfo { hasNextPage endCursor }
      }
    } } }
  }`,
} as const
export type GitHubDocument = keyof typeof githubDocuments
