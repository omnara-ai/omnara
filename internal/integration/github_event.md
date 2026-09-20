# GitHub durable intake handoff

`httpapi.Server.GitHubEventsHandler()` handles `POST` at
`httpapi.GitHubEventsPath`:
`/api/integrations/github/{app_id}/events`.
The path ID is the positive, canonical numeric GitHub App ID. `httpapi/routes.go`
mounts the handler and classifies it as provider-signed in
`serverManualRouteContracts`. The existing auth and OpenAPI middleware bypass
this non-`/api/v1` provider path; the mounted-route test exercises that stack.

The URL serves every installation of that physical GitHub App. The bounded body's
installation ID is initially a lookup hint for
`ListProjectAppsByProviderIdentity(ctx, "github", appID, installationID, after, limit)`.
The handler walks UUID-keyset pages of 100 active saved apps, including independent
apps for the same installation in different projects. Each reads its own
project-available `github_app_credentials`, including granted org secrets, and
verifies the exact raw body. There is no credential deduplication on ordinary
fanout: every receiving project's grant and saved setup must authorize its receipt.

Credential App ID must equal the URL and the saved app's `ProviderTenantID`;
verified `installation.id` must equal `ProviderAccountRef`. Signed
`installation.app_id`, when present, must also match. Hook-target headers are not
authorization evidence: GitHub's HMAC covers the body, not headers.

Raw UTF-8 JSON is capped at 1 MiB. Exact verified bytes go to
`AcceptIntegrationReceipt` with the receiving project/app ID and receipt key
`github:<X-GitHub-Event>:<X-GitHub-Delivery>`. Both headers are bounded tokens.
Success requires every eligible verifying app's insert or duplicate to be durable.
Permanent ineligibility, revoked credentials and invalid keys skip only that app;
transient credential/database failures return 503 while healthy siblings can
commit. Unknown decrypt/KMS errors also remain retryable. Failure logs name the
saved app/project, setup revision, verification/intake stage and error type,
without logging error text, credentials or payloads. Reconnect a persistently
corrupt setup using valid credentials for the same identity, or disconnect that
app to let independent healthy apps continue. Repair then explicitly redeliver
failed GitHub deliveries; accepted siblings dedupe. Replays reuse each app's receipt key. Reads and persistence share a
five-second request budget, including a bounded body read. The handler performs
no provider I/O or agent admission.

Only physical-App pings and callbacks without a matching active installation app
use `ListGitHubWebhookCredentialApps(ctx, appID, limit)`. Its indexed query in
`storage/queries/github_webhooks.sql` returns at most 16 distinct live credential
references from saved apps, choosing representatives with project secret access.
Disconnected apps may supply a verification credential but cannot receive input.
Every candidate still goes through its project's current secret/grant check.
This bound does not cap ordinary fanout, and a known active installation cannot
borrow the fallback's unrelated credentials when its own verification fails.

A verified App ping returns 204 without an inbox receipt. Unknown or entirely
disconnected installations use the bounded fallback and acknowledge only after
verification, without creating an app or admitting input through another saved
app. Known active installation lifecycle callbacks are durably captured and
normalize to no agent input. Ping requires its signed health-probe shape; merely
relabeling an event header cannot turn ordinary input into a ping. Deleting or
disconnecting one saved app does not change the URL or revoke independent apps.
At least one credential-bearing saved app must exist for fallback verification;
the resolver cannot discover App IDs inside unreferenced encrypted secrets.

`cmd/worker` registers `integration.GitHubAppInboxProvider{}` under `"github"` in
the consumer's provider map. It implements the existing `Expand`/`DownloadFile` interface, with
no planned file downloads. `NormalizeGitHubAppEvent(app, raw)` is pure and
accepts only trusted, previously verified inbox payloads. It derives event kinds
from signed object shapes and actions, so changing unsigned event/delivery
headers on a replay cannot change routing or semantic event identity. It never
re-verifies old receipts with a rotated current webhook secret.

Verified setup persists bot account facts in the saved app's `ProviderIdentity`:

```json
{"bot_user_id":1234567,"bot_login":"customer-app[bot]"}
```

Create app metadata with an immutable name and definition first. Explicit
`POST /apps/{app_id}/setup` calls `github.Client.CheckAppIdentity`, including
reconnect with unchanged credentials. Ordinary metadata/launcher edits perform
no provider discovery and do not advance `SetupRevision`. Disconnect and delete
remain provider-independent. Reconnect may refresh the mutable bot login but
cannot replace the app's installation or verified bot identity.

Authenticated `GET /app` with the App JWT checks the configured App ID and returns
its slug; `GET /app/installations/{id}` checks installation ownership. A temporary
metadata-read installation token authenticates `GET /users/{slug}%5Bbot%5D`,
including for Enterprise Managed Users. The client checks bot ID, login and type;
callers cannot supply trusted bot facts. The App response's `owner.id` is not its
bot ID.

`ConfigureProjectApp` writes observations only for its explicit `AppID`, checking
`ExpectedSetupRevision`, the discovered credential version and current project
secret access under lifecycle gates. Private metadata records
`verified_credential_version_id`. Concurrent reconnect, deletion, rotation or
lost grants reject stale observations. Setup does not rediscover a saved app by
credentials or reserve a globally unique physical identity.

The metadata-only setup token is installation-wide because app setup
precedes agent tool configuration. It is never cached or reused by PR tools, whose
tokens remain `repository_ids` restricted. Once setup receives a usable token,
it attempts [`DELETE /installation/token`](https://docs.github.com/en/rest/apps/installations#revoke-an-installation-access-token)
on every exit, authenticated with that token. Cleanup uses the same operation
deadline and request hook, makes one attempt, emits no raw logs and preserves the
original discovery result. Revocation is best effort: expired/canceled contexts,
authority-hook failures or provider errors can leave the token valid until expiry.
No background retry or detached request is scheduled. The HTTP journey creates
credentials, apps, configs, profiles and launchers through public APIs, with local fake
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

The concrete event address is always `RepositoryID` plus PR number. Where a PR
object exists, its `base.repo.id` must equal the signed envelope's repository ID. Issue comments
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

- **Credential lookup:** ordinary deliveries page all active apps for the exact
  physical App/installation identity and verify each separately. Only ping or
  unknown/inactive-installation verification uses the 16-candidate
  `ListGitHubWebhookCredentialApps` fallback.
- **Credential rotation:** `AcceptIntegrationReceipt` fences active project/app
  state and project secret availability, but has no expected credential version
  or setup revision argument. Rotation between signature verification and commit
  may admit a receipt signed by the just-replaced secret. No atomic verified-key
  revision fence is claimed; an extra preflight read would not provide one.
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
independent disconnection, bounded fallback resolution, credential/installation
mismatches, header/body limits, per-app secret access and sanitized errors. It also covers
205 same-installation apps across pages, partial transient failure with healthy
sibling commits, permanent bad-app isolation and no fallback borrowing. Tests use
in-memory capability fakes and local requests; no provider production calls.
The database journey also forces initial receipt persistence to fail with 503,
then manually redelivers twice with the same delivery ID: exactly one receipt is
accepted and consumed. This checks recovery without assuming GitHub auto-retries.
