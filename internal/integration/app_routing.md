# App inbox routing

Provider intake verifies and captures a receipt before invoking `AppRouter`.
The adapter expands that immutable receipt into at most 64 ordered `AppEvent`
values outside database transactions. Each event supplies its semantic key,
concrete provider scope, verified actor, ordinary input blocks and explicit
delivery/cancellation policy. A receipt ID is not a semantic message ID.

`AppLaunchWorkflow` invokes registered app policy before `Freeze`. Only that
policy supplies explicit `AppLaunchIntent` values; provider normalization cannot
choose agents. Each intent carries an app, stable slot and expected profile or
agent identity. Generic planning checks those identities against the live setup.
Slack/Discord select one profile, while GitHub explicitly selects every slot.
An exact continuation suppresses new profile choices in the app policy, not in
generic admission; a previously chosen request can still launch independently
when another setup has already started an agent in the same conversation.
Overlapping saved setups for one bot/conversation are misconfiguration; their
launch and delivery outcomes are unspecified, not a cross-app fan-out guarantee.

Chat profile menus use `app_profile_choices` before any config derivation or agent
creation. They retain the original normalized event, verified payload and offered
profile identities. A signed selection atomically records the winner and inserts
an ordinary inbox receipt with optional normalized `events`; provider receipts
cannot set that field. The receipt key is only an idempotency key. Selected work
skips expansion and app policy, then passes through normal `Freeze` and admission.
It is directed only to its chosen setup, so a late click cannot replay the
original message into newly matching listeners. Source and payload updates for
Slack attachment siblings are fenced together. Expected file digests are retained;
bytes are re-read on admission, and unavailable or changed files fail explicitly.
Selected exact siblings can reach the settled recipient without a listener;
this does not subscribe it to later messages.

Users should choose before sending follow-ups. Routing uses current listeners
when the receipt is planned, not a historical selection-time cutoff. Messages
processed before selection may be missed; delayed receipts can reach a listener
that is active by the time they are planned. There is no retrospective backfill.

Menus expire after one hour, but accepted selections remain retryable after that
deadline. Pending choices and accepted, unfinished launches prevent a second menu
for that setup. Once accepted, even before a frozen plan exists, zero-recipient
follow-ups retry through the same conversation reservation check. Failed work
without a plan allows a fresh request; failed frozen plans retain their existing
reservation until recovery. A retried request can reuse a settled agent only if
its launch profile still matches the captured choice. Unusable unselected menus
expire immediately, preserving their source replay barrier while allowing a new
mention to start again. For live scopes, maintenance retains choice bookkeeping
for at least seven days after expiry and while a selected receipt is pending,
processing or failed. Deleted connections, projects and organizations release
that bookkeeping early; disabling a connection preserves recovery data.
Only the creating receipt may publish a menu; sibling receipts enrich its source
without becoming publishers. The existing inbox lease and recovery govern that
receipt. Provider menu delivery can be repeated after an uncertain failure; only
a confirmed, recorded menu can choose, and one choice admits at most one profile.
This is not an exactly-once promise for posting menus.

`Freeze(ctx, lease, events)` reads live routing under connection, receipt and
sorted conversation gates. It reads each profile's current immutable compiled
config once outside those transactions, derives only the added app resources,
then checks routing again before freezing the entire plan. Concurrent routing
changes return `ErrAppRoutingChanged`; another unfinished/failed selection blocks
a new launch with `ErrAppSelectionReserved` and its owner receipt identity.
Retained selections prevent replacement launches even after
their listener or agent has retired. Broad parent listeners do not suppress
unrelated conversations.
Before freezing any event with zero recipients, the same transaction probes
the reservation index with `{connection_id,address}`, deliberately omitting
`app_id`. Pending and processing owners return `ErrAppSelectionReserved`
with their receipt/state. A plain follow-up therefore survives the interval
before the first listener exists. Failed owners do not delay zero-recipient
events: these complete empty without exhausting retries or accumulating failed
payloads. Failed owners still block replacement launch selections until explicit
operator recovery. Filtering happens before the reservation query's limit so
failed owners cannot hide a live reservation for another app at the same address.
Already admitted listeners can receive a follow-up while other slots remain
unsettled, without promising retrospective delivery to those later agents.

Only profile slots have the common `selection` envelope. The first plan reserves
all profile slots for an app/connection/conversation together. Each has a UUIDv7
agent, pinned base config ID/hash and a complete `Launch.DerivedConfig` snapshot.
That snapshot is the launch authority; its original source is deliberately absent
because source would describe the unmodified profile. Recovery never recompiles
source, re-resolves profile names, or substitutes newly edited app slots. Derived
configs are persisted only inside successful agent admission. Existing model,
machine, skill and subagent identities and global tool policies remain pinned.

The frozen plan is already an accepted selection. Disabling/editing the app
after freeze stops new selections, but does not gate admission of its frozen
N-slot membership, including after `RetryFailedIntegrationInbox`. Profile slots
retain app/slot/connection/address provenance in `selection`, the original
`ProfileID`, `BaseConfigID`/hash, and the complete derived compiled config. They
do not reload app settings or the profile's current config. The kernel still
requires a live same-project profile, usable model/config references, active
project/connections and ordinary launch/resource limits. A changed profile name
can affect a default agent name; it cannot replace the pinned configuration.

AgentID slots are ordinary event triggers. They have no selection, derived config
or subscription mutation. Overlapping triggers/listeners produce one input per
semantic event/agent. Listener-only recipients retain alternatives that admission
checks against current subscriptions under the agent lifecycle gate. Removing
receive authority blocks uncommitted delivery; unrelated config edits may keep
the subscription. Already committed semantic events replay without restoring
old listeners, changing interaction selection, or canceling new interactions.
AgentID trigger slots intentionally retain no app reference: their frozen agent,
content, actor, connection/address origin and semantic key are the accepted
ordinary input. Disabling the trigger app afterwards does not revoke that input
or rewrite the agent config. Live agent/project/connection and ordinary input
admission checks still apply. When a listener also matched, an explicit trigger
wins that event's admission policy; listener-only slots retain their alternatives
and must match a currently authorized listener/source config. None of these
retry rules recreate app authority or subscriptions on already committed replay.

Media refs supplied to planning use placeholder UUIDs and stable provider file
IDs. Each recipient receives distinct frozen artifact UUIDv7 identities, returned
in `Files` with their provider file IDs. The adapter downloads and uploads outside
transactions at those fixed identities, then calls `Prepare` with verified
artifact metadata/digests. Retries reuse the plan and append-only preparation.
The router performs no provider or blob network I/O.

`NewAppInboxConsumer(router, inbox, artifacts, providers, presenter, launchers)` accepts a provider map
of `AppInboxProvider` implementations. The Slack adapter is constructed with
`NewSlackAppInboxProvider(slack.OAuthConfig, secretStore, integrationStore, executionStore)`.
It reads `KindSlackAppCredentials` through `ReadProjectAvailableSecretPayload`,
supporting project-owned and granted credentials. After key unwrapping and before
each HTTP request it rechecks the active connection/revision and the project's
secret availability/version. History and label enrichment remain best effort,
but cannot hide a revoked grant or connection. No transaction spans these reads
and provider calls; admission independently rechecks current authority.

The scheduler entrypoint is:

```go
worker := integration.NewAppInboxWorker(integrations, consumer,
    integration.AppInboxWorkerOptions{
        Log: log, Capacity: 4, MachinePools: machinePoolManager,
    })
err := worker.Run(ctx)
```

`RunOnce(ctx) (bool, error)` recovers expired leases when due, discovers ready connections,
and consumes at most one claimed receipt, including partial/failed work in the
returned bool. `Run` bounds concurrent consumption (default four), stops claiming
on cancellation and waits for consumers to release still-owned leases. A claim
lasts five minutes; the consumer deadline leaves fifteen seconds for retry
persistence. Expired/stolen claims remain fenced even if provider I/O returns late.
All consumers share a discovery queue, taking one connection per round in the
store's returned order. Multiple consumers can claim different receipts on the
same connection; a slow attachment cannot serialize an entire workspace. Claims
and conversation gates provide correctness across consumers/replicas. Stale discovery
is retried on the next poll rather than repeatedly scanning in the same call.
Recovery runs independently while every consumer is busy and at most once per
30 seconds per worker instance, including calls through `RunOnce`. Receipt volume
therefore does not multiply inactive-scope/expired-lease recovery scans.

Storage follow-up: the current ready query limits the raw frontier to 100
receipts, then returns unordered distinct connections. The worker preserves its
order but cannot recover oldest-first ordering or discover a cold connection
hidden behind 100 older receipts from one hot connection. The bounded fix needs
keyset paging on `(available_at, id)`, retaining the cursor of the raw frontier
even when every entry belongs to an inactive scope. Sorting all pending work or
probing every connection on each event would defeat the bounded access path.
Until this store/query change lands, connection rotation is fair only within the
discovered frontier; global fairness is not claimed.

Successful launch results start the existing machine provisioning workflow even
when another recipient failed. Provisioning retains its own durable recovery.
Consumption failures preserve frozen plans and committed slots through
the store's bounded retry policy (five-second exponential delay, at most five
minutes). Attempt eight becomes a diagnosable failed receipt; explicit operator
retry retains identities/progress. Pending/processing reservations keep plain
follow-ups waiting. Recovery also runs when there are no ready connections.
Maintenance removes completed or explicitly discarded receipts after seven days
using database time, with at most 100 terminal and 100 deleted-scope receipts per tick.
Failed receipts on live or disabled connections remain recoverable. This is a
bounded transport dedupe window, separate from preserved agent history; see
[inbox retention](../storage/integrationstore/inbox_retention.md).

Direct `consumer.Consume(ctx, receipt.Lease())` remains available for a claimed
receipt and returns authoritative cancellation/provisioning outcomes. Pass the
existing `InteractionPresenter{Store: store, HTTPClient: client}` and the registered
`AppLaunchWorkflow` as explicit constructor arguments. `DismissCanceled` runs after committed input,
using each interaction's captured destination/receipt for either Slack or Discord.
Presentation failures do not retry accepted input. The Slack adapter also adds its
best-effort receipt reaction only for newly created input; semantic replay has no
new reaction or cancellation. Neither provider transport nor worker startup is
registered automatically by these constructors.

Hosted questions and approvals use the same presentation path. The worker polls
unattempted interactions; permission preparation also tries immediate background
scheduling, while questions rely on the polling worker. A full queue or a restart
before execution does not lose the notification. `Present`
atomically records `presentation_attempted_at` before provider I/O. Only that
claimant may send; provider-specific safe retries stay bounded within the attempt.
The marker never expires or resets. A crash after claiming can lose a notification,
and an uncertain send is not automatically repeated. Both remain answerable in
the dashboard. Confirmed receipts are persisted separately, including late
success after cancellation, without sending the message again. Customer-hosted integrations own
their presentation and recovery through the public interaction API.

Worker failures log receipt, connection, project, attempt, receipt age and durable
retry/failure outcome. Completed work older than one minute logs its age as well.
No provider payload or credential is logged.

Slack's asynchronous launch-failure journey retains the existing readable
`slack.AgentRequestFailureMessage` for capacity/admission rejection. A typed
`AppSlotAdmissionError` identifies eligible failed launches. Before provider I/O,
`ClaimLaunchFailureNotice` records one attempt in existing receipt progress; all
N recipients share that once-only attempt. A crash between claim and send can
lose the best-effort notice, but retries cannot spam the conversation. The receipt
and frozen plan remain diagnosable/retryable; a notice never commits a failed slot.

The consumer calls Discord `PrepareConversation` after freezing a nonempty plan
and before admission. Every pending slot includes its concrete typed `Scope`.
Before each HTTP request, the provider calls kernel
`CheckInboxConversationAuthority` for an accepting pending recipient at the exact
origin. The kernel holds sorted project/connection, receipt, conversation and then
profile/model or agent gates, checks live resources/listeners, and fences the lease
again after waits. It performs no writes and releases the transaction before I/O.
Frozen selections survive app disable as documented above. Empty plans and
completed replay never create a thread. Ordinary Discord replies require an exact
thread listener, including follows; channel/guild authority does not implicitly
subscribe every child thread. Parent addresses still select mention launchers.

Slack's `message` and `app_mention` callbacks share the message semantic key
`slack:message:<team>:<channel>:<ts>`. Attachment-bearing callbacks use the companion
`slack:message-files:<team>:<channel>:<ts>` key. The frozen `Sibling` field identifies
the companion and, for attachments, a neutral notice. Admission checks both under
the conversation/agent gates: files first suppress a later text callback; text
first permits one supplemental file input without repeating text or canceling an
interaction created after that text. Slot progress records the actual winning
input key, so either ordering replays without rewriting content or authority.

Ordinary channel roots can match explicit listeners without triggering a launch;
mentions can trigger launch, and DMs explicitly address the bot. Human messages
use steering and independently request interaction cancellation. Bot messages,
remote-team messages and message mutations are excluded. Lifecycle and name
events remain owned by verified intake.

A pure Slack prefilter completes an empty plan under conversation/reservation
gates before enrichment or downloads when there are no possible recipients.
Otherwise the consumer inspects/downloads bounded files before freezing, preserving skip
summaries for unsupported attachments. It pins digest, type, filename and size
alongside provider file IDs. No blob is uploaded before the selection and UUIDs
are frozen. Recovery checks existing uploaded bytes first, otherwise downloads
and verifies the same frozen facts. All writers at a planned object key must
match its frozen digest, including workers whose leases expire during upload.
Provider changes produce a diagnosable conflict, never an overwrite of different
content. Prepared/committed slots skip downloads and completed receipts replay
without credentials or active provider access.

`Admit(ctx, lease)` processes frozen slots in expansion order and attempts each
recipient independently. Profile admission atomically creates config, agent,
listeners, selection target, file metadata and first input with `CommitSlot`.
Existing-agent admission atomically commits attribution, file metadata, ordinary
input and slot progress. The receipt completes only after every slot commits;
partial failures return successful results and joined errors for the worker's
existing bounded retry/failure policy. Later events within one expansion can
continue newly planned agents only through listeners declared by their frozen
configs. They do not implicitly subscribe an agent.

The integrationstore planning bridge `AppRoutingCandidatesForInbox` uses the
lease handle's transaction, checks its lease before and after locking the supplied
conversation, and invokes `AppRoutingCandidatesTx`. It starts no other transaction
and locks no agents. Profile/config/connection resolution stays outside that callback.
