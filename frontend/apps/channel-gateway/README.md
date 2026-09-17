# Channel gateway

## Source layout

The gateway is one service and one TypeScript package. Direct imports connect
these directories; tests live beside the code they exercise:

- `src/core/`: authenticated calls to Omnara's control plane.
- `src/http/`: the HTTP server, webhook entrypoints, and HTTP lifecycle tests.
- `src/operations/`: synchronous send/read requests, envelope validation,
  multipart artifacts, and provider retry budgets.
- `src/consumers/`: processing of durable message and app-control receipts.
- `src/runtime/`: provider registration, configuration caching, and runtime leases.
- `src/slack/`, `src/discord/`, and `src/github/`: provider behavior and transport.

`src/index.ts` composes the service. Shared types and small HTTP, JSON, Redis,
logging, cancellation, and memory-budget primitives remain at the root. They do
not depend on the server or provider implementations. `src/testdata/` contains
the local provider-journey entrypoints.

## Runtime contracts

The first-party Slack path replays core-owned durable receipts. Core verifies the
public callback before storing and acknowledging it. Replay and input admission
use core's existing receipt and semantic idempotency. Outgoing operations remain
synchronous and bounded; an ambiguous publication is never blindly resent.

Slack runtime configuration contains only the installation bot token in
`slack_app_credentials.payload.access_token` and `install.provider_identity.bot_user_id`.
Core retains the combined stored secret, OAuth fields and webhook signing secret.
The gateway does not need those fields or an app runtime credential.

`OMNARA_CHANNEL_WEBHOOK_MAX_BUFFERED_BYTES` defaults to 256 MiB of shared retained
work accounting. Claims start at 64 KiB and grow with actual response bytes.
Media reservations grow with actual streamed bytes and include serialization
headroom; declared file sizes never replace stream limits. Existing accepted
input limits are 10 MiB per file, 24 MiB combined and 20 files. This budget includes
conservative allocation allowances rather than predicting exact process RSS.

Shutdown stops admission, lets active operations drain for the configured HTTP
shutdown period, then cancels remaining I/O. A provider callback that ignores
cancellation retains its reservation until it settles.

Private Go operation requests send `X-Omnara-Channel-Request-ID` as unpadded
base64url of the UTF-8 request ID (at most 342 header characters / 256 decoded
bytes). After authentication, a predispatch rejection can preserve its HTTP
4xx/503 status and return exactly `{request_id,outcome:"failed"}`. Header/body IDs
must match before dispatch. Invalid or duplicate headers reject without a
correlated result. Missing headers provide no correlation for early rejection.
Unmatched/proxy responses remain unknown to the sender; a started mutation's
ambiguous result remains unknown.

From `frontend/`, use `pnpm --filter @omnara/channel-gateway test` and
`pnpm --filter @omnara/channel-gateway typecheck`. `build:journey` builds the
test-only Node runners used by local Go/TS Slack, Discord and GitHub journey tests. No provider
credentials or production access are needed for the fake-provider unit tests.

The default binary also registers Discord and GitHub. Discord uses app-scoped
Gateway shards and Redis coordination; GitHub verifies HMAC webhooks at
`/hooks/<integration-app-id>/github/events`. Both feed the same durable core inbox
and invoke the same bound-input/workflow admission APIs as Slack. The gateway's
provider modules handle transport and built-in conversation behavior; core owns
bindings and transaction boundaries. Provider clients and SDKs implement transport
and provider-specific operations.

Discord capture queues charge the shared work budget for each retained dispatch
(at least 1 KiB, or eight times its serialized size). This also bounds queue
entries; there is no separate small message-count cutoff. Behavior filtering
happens after durable intake and does not reduce the capture queue or inbox.
Budget exhaustion stops the shard without advancing past uncaptured work. A
resumable session replays from the saved prefix; an invalid session cannot
guarantee recovery of unsaved traffic. Already-received queued messages survive
a session change while the runtime remains active, but only current-session
captures advance its checkpoint. See [Discord session resuming](https://docs.discord.com/developers/events/gateway#resuming).

All three providers share authenticated synchronous send/read operations and
per-request retry budgets. GitHub and Discord expose no interactive approval or
question presentation today; those remain in the Omnara dashboard. Disabling a
connection never deletes or publishes native messages or GitHub reviews.

GitHub expects a webhook response within ten seconds and does not automatically
redeliver failed deliveries. After an intake outage, an app owner can redeliver
failed webhooks from GitHub's recent deliveries; Omnara reuses delivery and input
identifiers for deduplication. Saved receipts retry independently of GitHub.
See [GitHub delivery guidance](https://docs.github.com/en/webhooks/using-webhooks/best-practices-for-using-webhooks)
and [redelivery](https://docs.github.com/en/webhooks/testing-and-troubleshooting-webhooks/redelivering-webhooks).

GitHub installation/access callbacks save app-scoped control receipts before
acknowledgment. These finite jobs are separate from project message receipts and
Discord's persistent runtime leases. They enumerate connections through a fixed
upper ID and checkpoint only confirmed forward progress. A partial pass resumes
after its last acknowledged connection, re-reading core revisions and provider
state before further writes. Successful passes yield; transient failures back
off without a total claim ceiling. Completion uses the original lease proof.
It never reuses a control receipt to admit an agent input.

The default control consumer has one worker and a child memory budget capped at
half the shared budget. The shared leased-receipt loop only owns scheduling,
cancellation and retained memory; the message and control wrappers keep their
own completion and retry policies. Connection reconciliation writes only Omnara
availability state, never GitHub comments, reviews or repository identity.

GitHub launch settings select automatic activation on PR opening (`pr_open`, the
default), or activation by a bot mention (`mention`). Mention activation accepts
PR descriptions on opening/editing and new or edited timeline/review comments
and published reviews. Deletes, dismissed reviews, and commit events cannot start
an agent. Once a PR workflow exists, supported later events continue its agent
regardless of the activation setting. Changing settings preserves route identity,
existing workflow agents, channel addresses, and grants.
