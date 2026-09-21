# Discord provider boundary

This package owns customer-bot REST, one Gateway connection per shard, message
normalization, and signed HTTP interactions. It owns no database, runtime lease,
subscription registration, tool authority, command registration, or application
selection. All tests use local HTTP/WebSocket fixtures; no live provider calls.

## Identity and credentials

The saved `ProjectAppRecord` stores the physical **application ID** as
`ProviderTenantID` and the separately verified **bot user ID** as
`ProviderAccountRef`. Both are immutable after first setup; do not infer one from
the other. `DiscoverIdentity` resolves both using the bot token and checks optional
customer-supplied IDs. Construct `NewClient` with both verified IDs.
`CheckIdentity` checks `/applications/@me` and `/users/@me`; Gateway `READY` checks
both too. **Guild ID** identifies a server and belongs to the concrete destination,
never app identity. A different application/bot identity needs another saved app.
The provider package receives credentials from its caller and never stores them.

Create metadata first with an immutable app name and definition. Explicit
`POST /apps/{app_id}/setup` discovers identity with documented
[`GET /users/@me`](https://docs.discord.com/developers/resources/user#get-current-user)
and [`GET /applications/@me`](https://docs.discord.com/developers/resources/application#get-current-application),
checks the bot flag, and persists observations with the checked credential version.
`ConfigureProjectApp` fences `ExpectedSetupRevision`, credential version and project
secret access after discovery. No provider I/O runs under storage locks.
Ordinary launcher/settings edits perform no discovery and do not advance
`SetupRevision`; reconnect explicitly targets the same app and re-verifies it.

Independent saved apps, including in different projects, may use the same
physical application/bot. They have separate setup, credentials, inboxes and
runtime leases. Setup never reserves a globally unique bot identity or discovers
an app by its credential. Token, public-key and transport changes advance setup
authority; unrelated metadata edits do not restart sessions.

The interaction public key is the application's Ed25519 key, not the bot token or
an HMAC secret. Pass it to `NewInteractionHandler`. Tools resolve destinations
from saved sending context or explicit arguments, then validate them with the
provider. Interaction destinations are independently selected. The bot
credentials and provider permissions are the access boundary, not a shared
resource-scope ACL.

`Scope{GuildID, ChannelID, ThreadID}` always separates the parent channel from an
optional thread. REST verifies the selected channel's guild and the thread's
parent before reads/writes. Guild ID may be omitted when authoring did not capture
it; channel/thread snowflakes still identify immutable resources. A selected
parent does not automatically subscribe all its child threads.

## Gateway decision and worker contract

Dependency: `github.com/discord-go/discord.go v0.13.1-static`, source commit
`8f18a6fe72f2576fdffe363ed18bd197e7fa472d`, checked September 18, 2026. This is the
`discord-go/discord.go` project, not `bwmarrin/discordgo`. It is a newer pre-v1 SDK;
the choice is based on the concrete public controls below, backed by local tests.
Its Gateway import also pulls `disgoorg/godave v0.3.0`,
`thomas-vilte/dave-go v0.5.1`, and `thomas-vilte/mls-go v1.6.0`. This package uses
none of their voice APIs. Existing `coder/websocket v1.8.15` supplies the thin
`gateway.Connection` transport adapter. We do not implement Gateway protocol,
fork the SDK, access private fields, or synthesize terminal dialer errors.

Supported API evidence:

- [`gateway.Client.Start` and `ConnFactory`](https://github.com/discord-go/discord.go/blob/8f18a6fe72f2576fdffe363ed18bd197e7fa472d/gateway/client.go):
  the single reconnect path returns its error when the public factory is nil.
- [`Session.SetSessionID`, `SetResumeURL`, `UpdateSequence`](https://github.com/discord-go/discord.go/blob/8f18a6fe72f2576fdffe363ed18bd197e7fa472d/gateway/session.go)
  restore a persisted checkpoint without private state access.
- [`Dispatcher.Dispatch`](https://github.com/discord-go/discord.go/blob/8f18a6fe72f2576fdffe363ed18bd197e7fa472d/gateway/dispatcher.go)
  invokes handlers synchronously;
  [`readLoop`](https://github.com/discord-go/discord.go/blob/8f18a6fe72f2576fdffe363ed18bd197e7fa472d/gateway/events.go)
  calls it before reading another event. The SDK advances its received sequence
  first, so that value is deliberately never exported as a durable checkpoint.
- [SDK low-level Gateway documentation](https://discord-go.github.io/discord.go/low-level/gateway/)
  documents the connection interface, client fields, and session configuration.

Previously evaluated alternatives: disgo commit
`b61a46a3b8aa7f21e6bff103e9f7abf597fe94dd` has `AutoReconnect=false`, but heartbeat,
opcode 7 and opcode 9 paths bypass it. `bwmarrin/discordgo v0.29.0` has synchronous
events and reconnect disabling, but no public persisted-sequence restore API.
Arikawa v3.6.0 resumes publicly, but buffered delivery and reconnect controls do
not provide the required commit boundary. These do not justify a fake-error guard
or an SDK-internals fork.

```go
type CommitDispatch func(context.Context, Dispatch, Checkpoint) error

func RunShard(context.Context, ShardConfig, *Checkpoint, CommitDispatch) error
```

The worker must:

1. Own a fenced app/shard lease. Prevent competing workers for that saved app's
   shard. Independent saved apps may run separate physical sessions for the same
   bot; do not merge their leases or receipts.
2. Load credentials and the latest durable checkpoint after acquiring the lease.
   Checkpoints include application ID, bot ID and shard topology; app setup and
   credential-version fencing remain the caller's responsibility.
3. Fetch `GetGatewayBot` metadata and honor its session start limits. Provide
   `BeforeIdentify` to acquire the bot-wide budget and the
   `shard_id % max_concurrency` bucket permit immediately before IDENTIFY is sent.
   RESUME does not consume that permit. `BeforeConnect` can recheck the lease.
4. In `CommitDispatch`, atomically persist the bounded dispatch (or an explicit
   irrelevant-event decision) and the proposed checkpoint under the lease fence.
   Include READY/RESUMED and ignored events in checkpoint handling. Do not enqueue
   in memory and return success. Honor the callback context; do not perform slow
   provider work inside this transaction.
5. Cancel the run on lease loss, app disconnect, setup change or credential rotation. On any exit,
   dispose of it and reload storage before another run. A callback error is
   returned unchanged; a callback panic becomes a sanitized error. Neither can
   advance the local checkpoint or permit another dispatch.
6. Interpret `GatewayError`: fatal close codes need corrected credentials/intents
   or shard configuration. `ResetSession` requires recording a non-resumable gap
   and clearing the old checkpoint before IDENTIFY. Respect `RetryAfter` outside
   the client. Ordinary Discord session expiry/replay-buffer gaps remain possible;
   this is not exactly-once delivery.

The SDK owns heartbeat sequencing. Heartbeats report received sequence, which is
not durable acknowledgement. Only the caller's stored checkpoint is used for a
subsequent RESUME. Repeated HELLO is rejected so the same run cannot resume from
an SDK-advanced sequence. Gateway frames/intake payloads are bounded at 2 MiB;
oversized guild dispatches cause a disconnect rather than being silently skipped.

Known upstream limitation: the pinned SDK starts its heartbeat ticker after a
full interval, rather than using Discord's recommended randomized first delay.
ACK monitoring and reconnect behavior are covered locally. Track the first-delay
fix when upgrading the SDK; do not add a second heartbeat loop in this wrapper.
See the pinned [heartbeater](https://github.com/discord-go/discord.go/blob/8f18a6fe72f2576fdffe363ed18bd197e7fa472d/gateway/heartbeat.go)
and [Discord's heartbeat interval contract](https://docs.discord.com/developers/events/gateway#heartbeat-interval).

`gateway_test.go` exercises actual SDK runs over local WebSockets: persisted
RESUME, READY identity, blocking intake, failed commit, panic/cancellation,
reconnect/invalid-session opcodes, close codes, and heartbeat loss. Each run dials
once. A fresh run after failed sequence 11 resumes from stored sequence 10.

## Mention, reply, tools and files

`NormalizeMessage` exposes actor, self/bot/webhook facts and mentions using **bot
user ID**. The app adapter in `../discord_event.go` excludes self/automated events
and verifies current channel metadata before producing concrete conversation
addresses. `AppRouter` matches app-owned subscriptions: a verified root mention
may use its source channel subscription, while replies require an exact thread
subscription. App launcher policy separately decides whether to launch a profile
or send to an explicit existing agent. Frozen subscription inputs recheck live
receive authority during admission before creating agent input. See
[Discord event routing](../discord_event.md) and [app routing](../app_routing.md).
App-owned `thread_messages` subscriptions attach one conversation and event set
to an agent independently of sending tools. Confirmed, explicitly requested
`follow_replies` sends require posting authority, without a receive grant in
agent config. Hosted launch admission adds its frozen conversation and resolved
events atomically, with no tool-call ID. Removing a sending tool preserves
subscriptions; deleting a subscription stops forwarding.

The inbox worker calls `EnsureThread` after durable intake. For a parent mention,
it creates or reuses the source message's single thread. A mention inside a
selected thread reuses it. Discord assigns a message-created thread the source
message ID; an uncertain create or already-created error is reconciled by GET,
never a blind POST retry. No archive, unarchive, delete, join or command mutation
is performed automatically on stop.

Map `app__<name>__read` to `ListMessages`/`GetMessage`, and
`app__<name>__post_message` to `CreateMessage`. Persist a stable, unique-per-bot logical
send nonce (1–25 ASCII token characters). All create attempts use the exact same
payload with `enforce_nonce=true`, at most three times inside one 15-second budget.
Discord's deduplication lasts only a few minutes: a final `DeliveryUnknown` is not
authorization to start a fresh retry later. No outgoing outbox is introduced.
`EditMessage` addresses a confirmed receipt directly and does not retry uncertain
edits. `BeforeRequest` rechecks caller authority before every HTTP attempt; before
an initial send its trusted error returns unchanged. After an uncertain send,
failure of that hook retains `DeliveryUnknown`.

Rate limits return immediately with `RetryAfter` and `Global`; caller scheduling
must coordinate per-bot limits across concurrent tools. GETs retry bounded safe
failures. Other mutation failures are not blindly retried. Responses are bounded
at 2 MiB, error bodies at 8 KiB, and errors exclude provider/transport text.
Redirects never forward credentials. No arbitrary pagination URLs are followed.

`MessageArgs.Files` uses bounded multipart uploads, at most 10 files, 8 MiB each,
25 MiB including the full request body. `DownloadAttachment` refreshes metadata
from the scoped message, checks attachment membership and CDN path/origin, and
downloads at most 8 MiB without bot auth or redirects. Artifact access control,
aggregate intake limits and storage belong to the caller. Content-type validation
and filename-to-artifact mapping also remain caller responsibilities.

Enable Guilds, Guild Messages and the privileged Message Content intent in the
customer application. This slice requests exactly those intents. The developer
portal must allow Message Content; otherwise Gateway may close with 4014.
An empty message alone is not proof that an intent is missing. Guild permissions
must allow viewing channels, reading history, sending messages, creating public
threads, sending in threads, and attaching files when used. DMs are supported by
REST scope validation but not subscribed by this guild-thread Gateway slice.

## Signed interactions

Omnara uses **signed HTTP interactions** for question and permission callbacks.
Gateway intake persists `MESSAGE_CREATE` and its checkpoint; it does not enqueue
or handle `INTERACTION_CREATE`. If such an event arrives before endpoint setup,
the worker records only the ignored-event checkpoint. There is no Gateway
callback fallback or queue of expiring interaction tokens. Discord makes HTTP
and Gateway interaction delivery mutually exclusive; ordinary messages continue
through Gateway. See [Discord's transport contract](https://docs.discord.com/developers/interactions/receiving-and-responding#receiving-an-interaction).

The shared control-plane route is
`POST /api/integrations/discord/{application_id}/interactions`, wrapping
`NewInteractionHandler`. The path carries the physical Discord application ID,
not an Omnara app ID. Prompts and profile choices select their single captured
saved-app owner through indexed metadata lookup. This untrusted lookup only
selects a candidate: that exact active app's `provider_config.public_key` then
verifies timestamp, raw body and application identity before any action is trusted.
Sibling app credentials cannot authorize the callback. PING has no owner, so it
pages active apps for the physical application and can use any matching valid
setup with current project credential access. The API adapter is
[`discord_interaction_routes.go`](../../httpapi/discord_interaction_routes.go).

Every configured Discord interaction handler requires a valid `public_key`
(64 hexadecimal characters encoding the application's 32-byte Ed25519 key).
Activation checks the app's key; presentation and callbacks recheck live setup.
Message-only apps may omit it. Removing the key makes hosted mirrors unavailable;
the dashboard interaction remains open. Any verified participant in the captured
conversation may answer; there is no separate approver ACL.

### Endpoint setup

Configure one endpoint for the physical Discord application:

1. Open the customer's application in the Discord Developer Portal. Copy its
   **Public Key** into the saved app's setup `provider_config.public_key`.
2. Expose the API route at a public HTTPS URL. In the application's **General
   Information** page, set **Interactions Endpoint URL** to
   `https://<omnara-api-host>/api/integrations/discord/<application_id>/interactions` and save.
3. Discord validates the endpoint using a signed `PING`. The route must return
   HTTP 200 with JSON `{"type":1}` for a verified ping and reject invalid
   signatures with HTTP 401. Independent saved apps share this URL and configure their own keys.

These steps follow [Discord's endpoint setup documentation](https://docs.discord.com/developers/interactions/overview#configuring-an-interactions-endpoint-url).
URL provisioning remains an operator setup step; Omnara does not mutate the
customer's application configuration or register slash commands for these
message-component callbacks.

Ed25519 verification covers the exact timestamp plus raw request body, bounded
at 1 MiB, with a five-minute clock window. Ping is verified and acknowledged
directly. Slash commands, component actions and modal submissions invoke the
synchronous intake callback with a two-second context, before acknowledgement.
Answer callbacks validate the signed message/channel against the captured
interaction, then call `executionstore.ResolveAgentInteractionFromHandler`.
That method fences the app and authenticated `SourceSetupRevision` before
locking the agent, then checks the live handler against the captured app,
arguments and destination. Profile-choice callbacks similarly verify their
captured owner and live setup revision before queuing one app-local inbox receipt. Commit the atomic core
resolution before acknowledging; do not defer resolution to the Gateway inbox.
The two-second context leaves room for Discord's three-second response deadline.
The HTTP handler cannot rescue a callback that ignores context. A duplicate
submission must not resolve an interaction twice, including after a committed
answer loses its HTTP acknowledgement. Opening a modal must derive the same
response from the captured form on replay; it does not resolve the interaction.

`EncodeCustomID` produces `om1:<int_…>:<action>` (under 100 characters); no scope,
credentials or permission decision is encoded. Buttons and modal text inputs are
supported. `Acknowledge` returns a deferred component update or a final ephemeral
receipt for slash/modal submissions. A callback may instead return a typed modal
response. Autocomplete, arbitrary component trees and interaction-token followup
workflows are outside this slice. Interaction tokens are intentionally absent
from the normalized intake type. Presentation uses ordinary scoped bot messages
and persisted receipts. Allowed mentions default to an empty parse list.

## Primary protocol references

- [Gateway and session start limits](https://docs.discord.com/developers/events/gateway)
- [Create message, nonce and files](https://docs.discord.com/developers/resources/message#create-message)
- [Start thread from message](https://docs.discord.com/developers/resources/channel#start-thread-from-message)
- [Rate limits](https://docs.discord.com/developers/topics/rate-limits)
- [Ed25519 request verification](https://docs.discord.com/developers/interactions/overview#validating-security-request-headers)
- [Text input required defaults and limits](https://docs.discord.com/developers/components/reference#text-input)
- [Interaction acknowledgement deadlines](https://docs.discord.com/developers/interactions/receiving-and-responding)
- [Current application identity](https://docs.discord.com/developers/resources/application#get-current-application)

Checked September 18, 2026. The source-control commits and local tests above are
the reproducible evidence for SDK behavior; protocol limits come from these
primary Discord references.
