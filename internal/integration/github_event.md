# GitHub durable intake handoff

`httpapi.Server.GitHubEventsHandler()` handles `POST` at
`httpapi.GitHubEventsPath`:
`/api/integrations/github/{app_id}/events`.
The path ID is the positive, canonical numeric GitHub App ID. `httpapi/routes.go`
mounts the handler and classifies it as provider-signed in
`serverManualRouteContracts`. The existing auth and OpenAPI middleware bypass
this non-`/api/v1` provider path; the mounted-route test exercises that stack.

The URL serves every installation of that physical App. The bounded body's
installation ID is initially only a lookup hint for the existing globally unique
`(github, appID, installationID)` connection. That connection selects a
project-available `github_app_credentials` secret, including granted org secrets.
Credential App ID must equal both the URL and immutable connection
`ProviderTenantID`; verified `installation.id` must equal `ProviderAccountRef`.
When present, signed `installation.app_id` must also match. Hook-target headers are not
authorization evidence: GitHub's HMAC covers the raw body, not headers.

Raw UTF-8 JSON is capped at the inbox's 1 MiB limit. The exact verified bytes are
passed to `AcceptIntegrationReceipt`, with receipt key
`github:<X-GitHub-Event>:<X-GitHub-Delivery>`. Both header values are bounded tokens.
Known active installation events return 204 only after the store accepts an
insert or duplicate; failed commits return 503. Database calls inherit a five-second request context;
the HTTP server remains responsible for socket/read deadlines. No provider I/O
or agent admission occurs in the HTTP handler.

Pings have no installation. The server wires
`ListGitHubWebhookCredentialConnections(ctx, appID, limit)` directly from
integrationstore. Its bound SQL query in `storage/queries/github_webhooks.sql`
selects at most 16 distinct live credential references from existing connections,
with a representative whose undeleted project has direct or granted secret access.
The store validates canonical positive App IDs and a limit of 1–16. It includes
disabled connections when their credentials remain accessible:
installation intake state is independent of the App's signature-verification
credential. The handler still reads each credential through the representative
project's current availability/grant check, verifies its App ID and HMAC, and
does not expose candidate details. It never scans the whole connection table or
decrypts an unbounded list. This is a required composition dependency, not a
fallback to a particular installation. No App table or registration is created.

A verified ping returns 204 without a project inbox receipt: it is an App health
probe, not a project input. Unknown-installation callbacks use the same App
signature resolver and return 204 only after verification, without creating a
connection or admitting the event under another installation. Known disabled
installations verify through their own credential and acknowledge without input.
Known active installation lifecycle callbacks are durably captured but produce
no agent input in this normalizer. Disabling/deleting one connection does not
change the URL or the exact lookup for any other installation. Configure at least
one credential-bearing connection before testing the App ping; the helper cannot
discover an App ID inside encrypted secrets that have no connection reference.

`cmd/worker` registers `integration.GitHubAppInboxProvider{}` under `"github"` in
the consumer's provider map. It implements the existing `Expand`/`DownloadFile` interface, with
no planned file downloads. `NormalizeGitHubAppEvent(connection, raw)` is pure and
accepts only trusted, previously verified inbox payloads. It derives event kinds
from signed object shapes and actions, so changing unsigned event/delivery
headers on a replay cannot change routing or semantic event identity. It never
re-verifies old receipts with a rotated current webhook secret.

Connection creation persists verified bot account facts in `ProviderIdentity`:

```json
{"bot_user_id":1234567,"bot_login":"customer-app[bot]"}
```

The public connection create handler calls `github.Client.CheckAppIdentity`.
Every active PUT repeats verification, including when credentials are unchanged:
an ordinary save is the explicit refresh for a renamed App's mutable bot login.
Disabling an existing connection with the same credential reference skips provider
I/O, preserving the stored observations; deletion also remains provider-independent.
Re-enabling the connection refreshes its identity before saving.
Authenticated `GET /app` with the App JWT verifies the configured App ID and
returns its slug; `GET /app/installations/{id}` verifies installation ownership.
A temporary metadata-read installation token authenticates the
`GET /users/{slug}%5Bbot%5D` lookup, including for Enterprise Managed Users.
The client validates the returned bot ID, login and type. Storage persists these
observations against the same validated credential revision. Updates with fresh
observations also check the connection revision and reject a changed bot user ID.
Other updates preserve the observed identity. Callers cannot supply bot facts.
The App response's `owner.id` is not its bot ID.

Private metadata stores `verified_credential_version_id` for the checked secret
revision. Verified PUT updates carry `SourceVerifiedIdentityRevision`, observed before discovery and
checked under the connection lifecycle gate, plus the existing secret-version
and grant checks. Concurrent edits, deletion, rotation or lost grants reject the
stale observation. Disabling with the same credential reference preserves the
latest facts under that same gate; active settings saves require GitHub availability.

The metadata-only setup token is installation-wide because connection creation
precedes repository selection. It is never cached or reused by PR tools, whose
tokens remain `repository_ids` restricted. Once setup receives a usable token,
it attempts [`DELETE /installation/token`](https://docs.github.com/en/rest/apps/installations#revoke-an-installation-access-token)
on every exit, authenticated with that token. Cleanup uses the same operation
deadline and request hook, makes one attempt, emits no raw logs and preserves the
original discovery result. Revocation is best effort: expired/canceled contexts,
authority-hook failures or provider errors can leave the token valid until expiry.
No background retry or detached request is scheduled. The HTTP journey creates
credentials, connections, configs, profiles and launchers through public APIs, with local fake
GitHub endpoints for identity discovery and no provider-observation store bypass.
Evidence checked September 18, 2026:
[authenticated App endpoint](https://docs.github.com/en/rest/apps/apps#get-the-authenticated-app),
[installation ownership](https://docs.github.com/en/rest/apps/apps#get-an-installation-for-the-authenticated-app),
[authenticated user lookup](https://docs.github.com/en/rest/users/users#get-a-user)
and [GitHub's bot-ID lookup example](https://github.com/actions/create-github-app-token#configure-git-cli-for-an-apps-bot-user).
GitHub documents mutable App names in
[modifying an App registration](https://docs.github.com/en/apps/maintaining-github-apps/modifying-a-github-app-registration).

App ID, installation ID, and bot user ID are distinct. The bot login is needed
for mention detection and cannot be inferred from numeric App ID. Supported
inputs fail explicitly when bot identity is missing. Self-events are excluded
using bot user ID or login; human comment/review inputs require both sender and
author to be human and to have the same user ID. PR opens and commits from other
bots may queue, allowing dependency-update PRs.

| Signed event/action | Normalized kind | Delivery | Launch eligibility |
| --- | --- | --- | --- |
| `issue_comment/created` with `issue.pull_request` | `discussion_comment` | Steering, cancel open interactions | Explicit bot mention |
| `pull_request_review_comment/created` | `review_comment` | Steering, cancel open interactions | Explicit bot mention |
| `pull_request_review/submitted` | `review_comment` | Steering, cancel open interactions | Explicit bot mention |
| `pull_request/opened` | `pull_request_opened` | Queued | PR-open selection only |
| `pull_request/synchronize` | `commit` | Queued | None |

Scope is always `RepositoryID` plus PR number. Where a PR object exists, its
`base.repo.id` must equal the signed envelope's repository ID. Issue comments
instead use the signed repository envelope, issue number and PR marker, because
GitHub does not include the full PR object in that event. Display owner/name,
HTML URLs and diff paths never determine authority; no URL is fetched.

Semantic keys use repository ID and provider object ID/action, independent of
delivery headers or repository names. Synchronize adds before/after SHAs and PR
update time when supplied, so ordinary redelivery collapses while later repeated
force-push transitions can remain distinct. New/reused names cannot retarget an
existing repository-ID selection. Metadata exposes inline/reply/review facts.

Mention matching is a case-insensitive textual `@slug` or `@slug[bot]` token,
with login/email boundaries. It is not GitHub's notification engine or a Markdown
parser: a token inside quoted/code text still counts. PR body mentions do not
turn PR-open events into mention launches. Edits/deletes, pending/dismissed
reviews, arbitrary push events without a proven PR, and installation lifecycle
events yield no agent input. This adapter never publishes reviews or mutates
provider state on stop.

## Remaining boundaries

- **Credential lookup:** `GitHubEventsHandler` uses the bounded
  `ListGitHubWebhookCredentialConnections` query over existing connections,
  secrets and grants for App-level verification. Normal deliveries use
  `GetIntegrationConnectionByProviderAccount`; no new identity table,
  per-project App registration or credential-owning installation is needed.
- **Credential rotation:** `AcceptIntegrationReceipt` fences active project and
  connection state, but currently has no expected credential version or connection
  revision argument. A rotation between verification and commit may admit a
  receipt signed by the just-replaced secret. Closing that race requires storage
  to atomically check the verified revision with the insert; an extra preflight
  read cannot provide that guarantee. No such atomic fence is claimed here.
- **Delivery recovery:** GitHub does not automatically redeliver failed webhooks.
  A 503 does not schedule a provider retry. Operators inspect GitHub's delivery
  history and manually redeliver after correcting the failure; Omnara has no
  automatic GitHub redelivery scheduler. After durable receipt acceptance, the
  worker processes/retries through the inbox independently of GitHub. This bounded
  handler rejects payloads larger than the inbox permits instead of acknowledging
  data that cannot be stored. HMAC has no timestamp; semantic dedupe and inbox
  retention govern replay handling, not a fabricated signature freshness window.

## Evidence and local checks

Primary references checked September 18, 2026:

- [GitHub signature verification](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries):
  HMAC-SHA256 over exact payload bytes, constant-time comparison, UTF-8.
- [Webhook events and payloads](https://docs.github.com/en/webhooks/webhook-events-and-payloads):
  installation summaries, issue PR markers, review/comment actions and PR events.
- [Webhook best practices](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks):
  ten-second response deadline, delivery identifiers, asynchronous processing.
- [Handling failed deliveries](https://docs.github.com/en/webhooks/using-webhooks/handling-failed-webhook-deliveries):
  explicit recovery/redelivery responsibility.

`github_event_test.go` covers stable repository/PR scope, rename/name reuse,
unsigned metadata replay, self/bot suppression, mention and PR-open selection,
steering/cancellation, queued commit transitions, malformed and cross-account
payloads. `httpapi/github_event_routes_test.go` covers signed raw capture, commit
ordering/failure, App ping verification, multi-installation/multi-project routing,
independent disablement, bounded credential resolution, credential and installation mismatches,
header/body limits, secret-read scope and sanitized errors. Tests use in-memory
capability fakes and local requests; no provider production calls.
The database journey also forces initial receipt persistence to fail with 503,
then manually redelivers twice with the same delivery ID: exactly one receipt is
accepted and consumed. This checks recovery without assuming GitHub auto-retries.
