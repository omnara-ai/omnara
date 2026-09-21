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

A receipt identifies the only app whose launchers, subscriptions and reservations
participate in routing. Conversation locks and semantic idempotency include its
app ID. Same-bot apps in one or different projects can independently launch and
receive in the same physical conversation.

Agent config has independent `tools` and `interaction_handlers` maps.
Compilation pins public project-app IDs. Qualified
tools use `app__<name>__<operation>`; handler keys use the app name. Whole authored
entries win during hosted derivation, including enabled state, permissions,
and deferred loading. Derivation only adds missing entries.

Tool schemas are static. An initial hosted target supplies immutable sending
context for its agent/app. Calls may omit the address; supplied fields must stay
inside that context. Channel contexts permit child threads; thread contexts do
not permit switching threads. Without context, tools require explicit addresses.
Credentials and provider validation remain the access boundary. Handlers take
complete destination arguments independently of sending context. Tools confer no
receive authority, and ordinary attribution never becomes a sending context.
Incoming model context carries the app name and actual provider address IDs.

An app-owned subscription connects one agent, a local definition type such as
`thread_messages` or `pull_request`, one concrete conversation and resolved events.
Config activation and tool removal do not reconcile subscriptions. Explicit
attachments and confirmed `follow_replies` sends use the same subscription store;
a follow requires the posting tool's authority and a confirmed send, with no
empty receive grant in config. Subscriptions own no credentials or transport.

Frozen input authority contains an event plus alternative `Type`/`Address`
references. Admission rechecks a matching live subscription under the agent gate.
Deleting a subscription blocks uncommitted forwarding while it is absent; a fresh
matching attachment may authorize the input. Subscription IDs are not ingress
generations. A committed replay cannot restore a deleted subscription. Stopped
forwarding does not relaunch a selected conversation: selection history remains,
and explicit reattachment resumes forwarding. App disconnection suspends delivery
while preserving subscriptions; app deletion and agent archival remove them.

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
cannot replay the original request into unrelated subscriptions. Slack attachment
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

Routing uses subscriptions present at planning time, with no retrospective backfill
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
replacement launches after their agent/subscription retires.

Each profile slot freezes a UUIDv7 agent, selection provenance, base config ID,
complete `Launch.DerivedConfig`, and concrete `Launch.Subscriptions`. Source text is
absent because it would describe the unmodified profile. Retries do not compile
source, resolve current profile names, or substitute edited launcher slots. They
preserve model, machine, skill, subagent and tool policy identities. Derived
configs are persisted only during successful admission. Editing launcher settings
does not rewrite frozen membership; live project/app/credential, profile and
ordinary launch limits still govern unfinished work. Disconnect revokes new
provider work while keeping the frozen plan available for recovery after reconnect.

Hosted launchers supply tools and an optional handler. Admission designates the
selected target as immutable sending context. Each launch separately freezes one
attachment containing the app ID,
local subscription type, concrete conversation and resolved default events from
the app definition. Atomic admission writes this subscription before the initial
input and first step. It has no tool-call ID. Later messages and media in the same
expansion use these actual frozen subscriptions, including their event filters,
even though the agent does not exist yet. Neither profile config nor current
defaults reconstruct receive authority on replay. Ordinary external initial input
creates no provider target; merely discovering a handler also creates none.

Existing-agent trigger slots freeze ordinary input without a profile selection,
derived config or subscription mutation. Overlapping subscription/trigger matches
produce one semantic input per agent. Explicit triggers use their frozen accepted
origin; subscription-only slots retain alternatives checked against live subscriptions
under the agent gate. Removing receive authority blocks uncommitted subscription
work, which follows the existing bounded retries and retained-failure recovery.
Committed semantic replay does not restore subscriptions, change handler selection
or cancel newer interactions.

## Admission, media and leases

The router performs no provider/blob I/O. Frozen slots allocate distinct artifact
UUIDv7 IDs per recipient and retain provider file IDs and expected digests. The
consumer downloads/uploads outside transactions, then appends verified preparation
with `Prepare`. Recovery reuses those IDs and verifies bytes, type, filename,
size and digest. Existing prepared/committed work skips downloads; conflicting
provider bytes fail visibly instead of overwriting frozen content.

`Admit(ctx, lease)` visits slots in expansion order and attempts independent
recipients even when another fails. Profile admission atomically commits derived
config, agent, launch subscriptions, selected target, artifacts, initial input
and slot progress. Existing-agent admission commits
attribution, artifacts, input and progress together. Lease expiry rolls everything
in that slot back. The receipt completes only when every slot commits; partial
successes and joined errors feed the existing retry policy. Later events in the
same expansion can continue newly planned agents through their frozen launch
subscriptions.

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
require an exact subscription; parent/guild launcher matching does not
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
cancellation. Dismissal uses the captured app, handler arguments and provider
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

## Scheduled thread launches

Cron is the only schedule owner. An app-launch firing locks project/app, profile
and cron in that order, rechecks its claim/lease, enabled state and due time, then
atomically snapshots the profile config and rendered task/opening into the app
inbox, records last_app_receipt_id and completes the firing. A stale occurrence
releases only its own claim and preserves a concurrent edit's next fire time.
Scheduled jobs are explicitly source=scheduled_launch; verified provider intake
cannot populate that discriminator. Mention settings authorize no part of this
handoff. The diagnostic last_app_receipt_id is an expiring reference, not authority;
reads must check project, app and source, and never choose an older retained run.

The worker posts the opening outside transactions, then saves its concrete thread
in FreezeScheduledLaunch's immutable plan. There is no separate publication-attempt
record or message-history reconciliation. Definite non-delivery can retry within
the existing inbox budget. Slack timeouts, server errors and missing acknowledgments
fail the run; Discord retains its bounded same-nonce send retries and treats a final
uncertain result as terminal.

An observed failure between successful publication and saving the plan is terminal,
so a deterministic planning error cannot post another heading on every inbox retry.
If a commit acknowledgement is lost, a visible saved plan is reused. Failures after
that commit resume from the saved thread. A crash, lease loss or database outage
before either the plan or terminal outcome is persisted may leave a stray heading
and permit a replacement on recovery. Explicit operator retry of a failed receipt
without a plan can also publish again. Atomic admission still creates at most one
agent per occurrence. This bounded delivery limitation is deliberate.

The frozen selection reserves the conversation before Discord EnsureThread runs.
Admission then uses the existing config/agent/subscription/initial-input transaction.
Its Omnara cron actor is authorized from the locked scheduled receipt, not a flag
in provider JSON or plan JSON. The thread origin selects its interaction handler.
Retries of committed admission do not repeat provider preparation.

Slack has a residual publication-before-commit window: a reply can arrive before
the conversation reservation exists and be treated as unrouted. A crash can widen
that interval. The existing bounded retry/reservation design protects the period
after freeze; it does not promise atomicity between Slack and PostgreSQL. No
channel-wide hold or second outbound queue is introduced for that rare gap.

App disconnect/delete and profile deletion fence remaining work. Disabling or
deleting the schedule affects future firings; accepted receipts keep their
snapshot. A completed receipt means the agent was launched, not that its eventual
report was delivered. Scheduled failures are surfaced by the cron's exact retained
last_run, separately from handoff failure_report. No new table or per-agent mutable
app configuration is involved.
