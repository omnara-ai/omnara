# App inbox routing

## Identity, intake and capability ownership

A `ProjectAppRecord` is the lifecycle owner for credentials, verified provider
identity, launcher settings and durable routing. Its name and definition are
immutable. Setup/reconnect explicitly targets its ID and checks both
`ExpectedSetupRevision` and the observed credential version. Metadata changes do
not advance `SetupRevision`; provider work and runtime leases use that revision,
never the app's general `UpdatedAt` timestamp.

Independent saved apps may use the same physical provider identity. Slack/GitHub
ordinary intake walks indexed, bounded pages of active matching apps. Every app
checks its own project credential access and signature, and stores its own receipt
before acknowledgement. Permanent ineligibility skips that app; transient errors
return non-2xx while healthy siblings can commit. Retry dedupe is app-local, with
no cross-project transaction or global credential deduplication. Discord Gateway
sessions independently commit dispatches under each app/shard lease. Its shared
physical-application HTTP endpoint handles signed interactions separately.

Slack's event-verification lookup also pages disconnected, non-deleted apps with
retained credentials. A candidate that verifies its own signature can acknowledge
`ignored` without an inbox write. Unknown disconnected-credential errors are
logged but cannot block another independently verified app's acknowledgement;
without any verified candidate they still return 503. Unknown active-app errors
always require retry. Reconnect reads the new setup and credential on the next
delivery; there is no identity or credential cache. GitHub and Discord retain
their existing active-only ordinary lookup.

A receipt identifies the only app whose launchers, listeners and reservations
participate in routing. Conversation locks and semantic idempotency include its
app ID. Same-bot apps in one or different projects can independently launch and
receive in the same physical conversation.

Agent config has independent `tools`, `listeners` and `interaction_handlers`
maps. Compilation pins public project-app IDs and immutable per-entry config.
Qualified tools use `app__<name>__<operation>`; listener keys use
`<name>__thread_messages` or `<name>__pull_request`; handler keys use the app name.
Whole authored entries win during hosted derivation, including enabled state,
permissions, deferred loading and config. Derivation only adds missing entries.

Tool/handler config fixes hidden arguments. Unspecified destination fields remain
runtime arguments subject to provider validation and credentials. There is no
shared resource object or generic scope-containment ACL. Tools confer no receive
authority, and attribution/selection targets confer neither tool nor listener
authority. Incoming model context carries the immutable app name and actual
provider address IDs so flexible tools can reply without guessing.

A named listener owns subscriptions with `Origin=configured` or `Origin=runtime`.
Its `conversations` seed configured subscriptions; an empty list seeds none.
Runtime follows attach a confirmed conversation to that existing listener and
use its current event selection. Reconciliation preserves runtime follows while
the same listener key remains pinned to the same app, even if its initial list or
sending tools change. Removing the listener revokes both origins. Admission
references contain `ListenerKey`, `Address` and `Origin` and must match the current
subscription; there is no separate followed boolean or tool-resource authority.

## Decisions, choices and frozen plans

Provider intake captures verified bytes before routing. `AppInboxProvider.Expand`
translates one receipt into at most 64 ordered `AppEvent` values outside database
transactions. Each has a semantic key, concrete address, verified actor, input
blocks and delivery/cancellation policy. Transport receipt IDs and semantic message
IDs can differ.

`AppLaunchWorkflow.Decide` invokes registered app policy before `Freeze`.
Normalization cannot choose agents. Explicit intents carry app, stable slot and
expected profile/agent identities, which planning validates against live setup.
Slack/Discord's `ChatAppLauncher` selects a sole profile directly or presents a
menu for several. GitHub's `EverySlotAppLauncher` explicitly selects configured
slots for mention or PR-open triggers. Exact existing continuations suppress new
profile selection within that app; another saved app's work does not suppress it.

Profile menus use `app_profile_choices`, retaining the original normalized event,
verified payload and offered profile identities before any config derivation or
agent creation. Signed selection resolves that captured app owner, verifies its
own credentials/message/destination and live setup revision, then atomically
records the winner and queues an ordinary app-local receipt. Owner lookup by
choice or interaction ID is bounded routing metadata, never authentication.
Callbacks are not ordinary webhook fanout.

Selected receipts carry trusted normalized `events`, skip expansion and policy,
and enter normal freeze/admission directed to the chosen app/profile. A late click
cannot replay the original request into unrelated listeners. Slack attachment
siblings update retained source and payload together; frozen file digests still
must match on admission. A selected exact sibling can reach its settled recipient
without granting a subscription to future messages.

Menus expire after an hour; accepted selections remain retryable after expiry.
Pending choices and accepted unfinished launches reserve that app/conversation.
Only the creating receipt publishes a menu; siblings may enrich its source.
An uncertain send may repeat a menu, but only a confirmed recorded menu can
choose, and one choice admits at most one profile. There is no exactly-once menu
send guarantee. Unusable unselected choices expire while preserving their source
replay barrier. Live-app choice bookkeeping is retained for at least seven days
after expiry and while selected work remains unfinished/failed. Disconnect keeps
recovery data; deleted apps/projects/organizations may release it earlier.

Routing uses listeners present at planning time, with no retrospective backfill
of messages processed before a human selection. Before freezing zero-recipient
work, storage checks pending/processing reservations for that app/address so a
follow-up can wait for initial launch admission. Failed reservations do not hold
plain zero-recipient follow-ups indefinitely, but still prevent replacement
launches until explicit recovery. Already admitted recipients can receive while
other slots remain unsettled.

`Freeze(ctx, lease, events)` reads routing under app, receipt and sorted
conversation gates. It loads each profile's pinned compiled config outside those
transactions, adds missing capabilities, then rechecks routing before freezing.
Changes return `ErrAppRoutingChanged`; reserved membership returns
`ErrAppSelectionReserved` with the owner receipt. Retained selections prevent
replacement launches after their agent/listener retires.

Each profile slot freezes a UUIDv7 agent, selection provenance, base config ID/hash,
complete `Launch.DerivedConfig`, and `InboxLaunchSlot.ListenerKey`. Source text is
absent because it would describe the unmodified profile. Retries do not compile
source, resolve current profile names, or substitute edited launcher slots. They
preserve model, machine, skill, subagent and tool policy identities. Derived
configs are persisted only during successful admission. Editing launcher settings
does not rewrite frozen membership; live project/app/credential, profile and
ordinary launch limits still govern unfinished work. Disconnect revokes new
provider work while keeping the frozen plan available for recovery after reconnect.

Hosted launchers supply tools and an optional handler with fixed launch-address
config, plus an empty named listener. An existing listener entry must already be
pinned to the same app and is never merged or overridden. During atomic launch
admission, `ListenerKey` registers the launch conversation as a runtime
subscription, without a tool-call ID. This works even when the profile supplied
an empty listener or different initial conversations. Ordinary external initial
input creates no provider target; merely discovering a handler also creates none.

Existing-agent trigger slots freeze ordinary input without a profile selection,
derived config or subscription mutation. Overlapping listener/trigger matches
produce one semantic input per agent. Explicit triggers use their frozen accepted
origin; listener-only slots retain alternatives checked against live subscriptions
under the agent gate. Removing receive authority blocks uncommitted listener
work. Committed semantic replay does not restore subscriptions, change handler
selection or cancel newer interactions.

## Admission, media and leases

The router performs no provider/blob I/O. Frozen slots allocate distinct artifact
UUIDv7 IDs per recipient and retain provider file IDs and expected digests. The
consumer downloads/uploads outside transactions, then appends verified preparation
with `Prepare`. Recovery reuses those IDs and verifies bytes, type, filename,
size and digest. Existing prepared/committed work skips downloads; conflicting
provider bytes fail visibly instead of overwriting frozen content.

`Admit(ctx, lease)` visits slots in expansion order and attempts independent
recipients even when another fails. Profile admission atomically commits derived
config, agent, configured listeners, launch runtime subscription, selected target,
artifacts, initial input and slot progress. Existing-agent admission commits
attribution, artifacts, input and progress together. Lease expiry rolls everything
in that slot back. The receipt completes only when every slot commits; partial
successes and joined errors feed the existing retry policy. Later events in the
same expansion can continue newly planned agents through their configured or
launch runtime subscriptions.

`NewAppInboxConsumer(router, inbox, artifacts, providers, presenter, launchers)`
requires explicit provider and launcher registrations. Provider credential reads
use `ReadProjectAvailableSecretPayload`, supporting project secrets and granted
org secrets. Before each provider request they recheck active app/setup revision
and secret availability/version. Best-effort history/labels cannot hide lost
credential authority. Admission separately rechecks current authority without
holding a transaction across network calls.
Slack inbox work and interaction presentation verify `auth.test` against the
saved workspace and bot user before enrichment, downloads or message sends.
A rotated token may continue with that same identity; another workspace, bot or
non-bot token is rejected.

Discord `PrepareConversation` runs only after freezing a nonempty plan. Before
each HTTP attempt, `CheckInboxConversationAuthority` checks an accepting pending
recipient at the exact origin under project/app, receipt, conversation and
profile/model or agent gates, fencing the lease after waits. It releases locks
before I/O. Empty plans and completed replay create no thread. Thread replies
require an exact listener subscription; parent/guild launcher matching does not
subscribe every child thread. See [discord_event.md](discord_event.md).

`NewAppInboxWorker` retains bounded concurrency (default four). `RunOnce` recovers
expired leases when due and consumes at most one claimed receipt. A claim lasts
five minutes; the consumer leaves fifteen seconds for retry persistence. Expired
or stolen leases remain fenced after late provider work. Workers share a ready-app
queue, taking one app per discovery round; separate consumers may process
different receipts for one app. Recovery runs independently of busy consumers at
most once per 30 seconds per worker, including calls through `RunOnce`.

The current ready query inspects at most 100 oldest pending receipts, then returns
distinct active apps without a promised order. Rotation is bounded to that
frontier; it does not guarantee global fairness to apps hidden behind a hot app's
older receipts. The worker cannot manufacture ordering or fairness absent from
that store query.

Failures preserve the plan and committed slots. Retry uses a five-second
exponential delay capped at five minutes; attempt eight becomes a diagnosable
failed receipt. Explicit retry retains identities/progress. Successful launches
start the existing independently recoverable machine provisioning workflow even
when another slot fails. Maintenance removes completed/discarded receipts after
seven days, with bounded terminal/deleted-app batches. Failed work on live or
disconnected apps remains recoverable. See
[inbox retention](../storage/integrationstore/inbox_retention.md).

Discord's persistent runtime separately retains app/shard leases, durable RESUME
checkpoints and app-local receipt/checkpoint transactions. Same-bot apps may run
independent physical sessions; bot-keyed IDENTIFY budgets/concurrency limits stay
shared. Setup or credential changes fence sessions; unrelated metadata/settings
changes do not. See [discord/README.md](discord/README.md).

## Presentation and provider-specific replay

`InteractionPresenter` polls unattempted hosted questions/approvals; immediate
permission scheduling uses the same claim. `presentation_attempted_at` is recorded
before I/O and never resets. A crash after claiming can lose a notification, and
uncertain sends are not automatically repeated; the dashboard/API remains usable.
Confirmed receipts are persisted separately, including late success after
cancellation. Dismissal uses the captured app, handler config/arguments and provider
receipt. A signed answer checks that exact owner and current setup revision and
commits core resolution before acknowledging. Sibling app credentials cannot
authorize it. Human steering cancels open interactions independently of delivery
mode; presentation failures never retry already accepted input.

Slack message/app-mention callbacks share
`slack:message:<team>:<channel>:<ts>`; attachment callbacks use
`slack:message-files:<team>:<channel>:<ts>`. Frozen sibling metadata makes files-first
suppress later text, or text-first admit one neutral supplemental file input
without repeating text or canceling an interaction created after it. Actual
winning input keys are retained for replay. Human messages steer; bot/self,
remote-team and mutation events are excluded. Lifecycle/name callbacks remain
owned by verified intake.

Slack prefiltering can complete an empty plan under reservation/conversation gates
before enrichment/downloads. Otherwise bounded files are inspected before freeze,
with explicit omission summaries. Planned object keys accept only bytes matching
the frozen digest, including late uploads after lease loss. A best-effort receipt
reaction occurs only for newly created input. Capacity/admission launch failures
use one durable `ClaimLaunchFailureNotice` attempt shared by all receipt slots;
a crash can lose the notice, but retries cannot spam the conversation.

Fanout failure logs identify `app_id`, `project_id`, provider, `setup_revision`,
`stage` (credential verification or intake), retryability and error type. They omit
raw error messages because storage/decryption errors may contain secret or row
contents. Unknown errors, including database/KMS/decryption failures, remain
retryable even after healthy sibling receipts commit.

For a persistently corrupt setup, use the logged app ID to inspect that app's
credential availability and reconnect it explicitly with valid credentials for
the same provider identity. Disconnect the affected app if it cannot be repaired
immediately; independent apps remain active. Do not turn an unclassified decrypt
failure into success to silence retries. After repair, redeliver failed webhooks
as the provider requires (GitHub needs explicit redelivery). Already accepted
sibling receipts dedupe. Disconnect preserves inbox recovery data; reconnect
restores access so failed durable work can be retried through the existing inbox
operator workflow.

Worker logs identify receipt, app, project, attempt and durable retry/failure
outcome, including old receipt age. They never include provider payloads or
credentials.
