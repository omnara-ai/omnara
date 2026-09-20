// Package github implements the bounded GitHub App transport for PR tools.
// Credentials belong to the customer's App; callers resolve the installation,
// authorize and map the current application scope to Scope, and recreate the client on
// credential revision. No credentials or tokens are persisted here.
//
// GetPullRequest, GetDiff, ListFiles, ListDiscussionComments and ListReviewComments
// support app__<name>__read. Each list reads one page (at most 100 items); callers
// must bound any aggregation and keep the original tool context across operations.
// CreateDiscussionComment, CreateInlineComment and Reply implement the three
// ordinary comment tools. There is no pending-review or stop-time mutation API.
//
// Each operation shares a 15-second deadline across authentication, repository
// resolution, PR identity validation, and its remaining requests.
// GETs get at most three attempts. Comment POSTs are never automatically retried:
// DeliveryUnknown means publication may have succeeded, even on timeout or a 5xx.
// RateLimited includes RetryAfter and returns immediately. Callers must preserve
// these outcomes rather than retrying an entire tool indiscriminately.
//
// Installation tokens use repository_ids to restrict access to one repository and pull_requests read
// or write. Pull requests write also permits ordinary PR discussion comments;
// contents and issues permissions are not requested. RepositoryID and PR number
// are authority; stored owner/name are display context only. Each operation uses
// GET /installation/repositories with the restricted token to resolve the current
// owner/name, requiring exactly the selected ID on one page. Unexpected additional
// repositories, missing access, or pagination fail rather than broadening access.
// Names are not cached alongside tokens. The client then uses documented /repos/
// routes and requires the PR response's base.repo.id and number to match Scope.
// Missing PR identity fails with InvalidResponse; a differing identity fails with
// ScopeMismatch. Replies also verify the comment and root comment's PR URL against
// the resolved repository/PR. The application owns ID-based routing/conversation
// keys and installation reassignment after transfers; this client never discovers
// or switches to another installation automatically.
//
// Read-isolation limitation: these checks are preflights, not an atomic repository
// ID condition on subsequent name-addressed REST requests. Diff bodies, file and
// comment pages omit a numeric repository identity; review comment URLs identify
// names/PR numbers, not repository IDs. A rename plus name reuse BETWEEN validation
// and a later read can therefore yield data from another PUBLIC repository: GitHub
// Apps retain public read access even with repository-restricted tokens. The client
// cannot detect that race from such a response, and does not claim to prevent it.
// Cross-repository PRIVATE reads and writes depend on GitHub enforcing the token's
// ID restriction at request time. The ID-bearing PR metadata response itself is
// always checked and discarded on mismatch. This package does not use the
// undocumented GET /repositories/{id} or infer ID-addressed PR/comment routes.
//
// Webhook receipt persistence,
// installation/project matching, self-event suppression, and steering versus
// background admission belong to the application. GitHub signatures cover the
// body, not delivery headers, and have no replay timestamp; deduplication remains
// the receiver's responsibility.
//
// Contracts checked against primary GitHub documentation on 2026-09-18:
// https://docs.github.com/en/apps/creating-github-apps/authenticating-with-a-github-app
// https://docs.github.com/en/rest/apps/apps#create-an-installation-access-token-for-an-app
// https://docs.github.com/en/rest/apps/installations#list-repositories-accessible-to-the-app-installation
// https://docs.github.com/en/apps/using-github-apps/installing-a-github-app-from-a-third-party
// https://docs.github.com/en/rest/pulls/pulls
// https://docs.github.com/en/rest/issues/comments
// https://docs.github.com/en/rest/pulls/comments
// https://docs.github.com/en/rest/using-the-rest-api/rate-limits-for-the-rest-api
// https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries
// https://docs.github.com/en/webhooks/webhook-events-and-payloads
// Official OpenAPI inspected at github/rest-api-description commit
// d4278c869e367f5d6d4e0f46878119128abba77b, descriptions/api.github.com/api.github.com.json.
package github
