# Contributing an Omnara integration

Built-in integrations are reviewed code shipped with Omnara. Customer-hosted integrations
use the [public input, custom-tool and interaction APIs](../../docs/integrations/custom-integrations.mdx).
For user setup, see [Integrations](../../docs/integrations/overview.mdx).

## Where to make a change

| Responsibility | Owner |
| --- | --- |
| Integration kind, capabilities, addresses and schedule schema | `internal/integrationdefinition` |
| Tool schemas | `internal/toolcatalog/integration_tools.go` |
| Config name resolution and pinned integration IDs | `internal/agentconfig`, `internal/agentconfigcompile` |
| Tool execution and credential checks | `internal/harness/tools/integration_tools.go`, `integration_authority.go` and provider tool files |
| Provider HTTP/socket protocol | `internal/integration/<provider>/` |
| Event normalization and launcher policy | Provider event files, `integration_launch.go` and `integration_profile_choice.go` |
| Setup and verified webhook/callback intake | `internal/httpapi` |
| Provider, launcher, state-work and scheduled-action registration | `cmd/worker` |
| Integration, subscription, workflow state, inbox and transport leases | `internal/storage/integrationstore` |
| Atomic agent launch/input and interaction resolution | `internal/storage/executionstore` |

Start with `slack_thread`, `discord_thread` or `github_pr`. Adding an operation to
an existing integration usually touches its definition, tool schema and provider executor.
A new integration kind also needs the PostgreSQL constraint and OpenAPI enum updated,
then regenerated contracts. Keep provider classification in the code registry;
several integration kinds can share a transport without another database column.

## Identity and capabilities

Use integration terminology for hosted behavior. The existing `/api/integrations/` callback
URLs, `itgt_`/`ioaf_` ID prefixes, input idempotency scopes and Slack button keys/markers
remain wire/storage contracts; renaming them would break existing references.
Integration tests and customer-hosted integrations retain their separate meaning.

A project integration owns its credentials, verified external identity and optional
launcher. Its name is immutable; compiled configs pin its UUID so deleting
and reusing a name cannot redirect an existing agent. Setup revision changes fence
provider setup, not ordinary launcher/settings edits. Independent integrations may use the
same physical bot; shared webhook infrastructure does not merge their ownership.

Source config refers to an integration named `support` like this:

```yaml
tools:
  int__support__read: {}
  int__support__post_message: {}
  list_interaction_handlers: {}
  set_interaction_handler: {}
interaction_handlers:
  support: {}
```

Launchers add missing capabilities and preserve existing settings, including
explicitly disabled tools and permissions. Slack and Discord add interaction
helpers; GitHub does not. Subagents do not inherit integration capabilities or subscriptions.
Reuse the common integration-tool access checks: pending calls must remain authorized by
both their original and current configs, and provider requests recheck live integration
setup and credential access.

Each tool declares its `IntegrationToolDefinition.Scope` in code. `IntegrationToolScopeIntegration` uses
the project's integration credentials without requiring a launcher or subscription.
`IntegrationToolScopeConversation` additionally requires the agent's assigned conversation.
Scope cannot be changed through config or model arguments. Both use the same
config, permission, credential and live-integration authorization checks. Integration-scoped tools
can reach anything their implementation permits within those credentials; review
any target arguments accordingly. Approval summaries include a destination only
for conversation-scoped tools.

The shipped integrations save one assigned conversation per agent/integration during launch in
`integration_states`, keyed by `agent_conversation` and the agent UUID. Its `{kind, ref}`
payload is insert-only through the owning storage API and commits with the launch.
The assignment survives subscription changes and agent archival; integration/project
teardown reclaims it with other integration state. All their current tools are
conversation-scoped and take action arguments, not destinations. A missing or
malformed assignment never grants unrestricted sending.
The model-facing tool description identifies this assigned conversation; subscribed inputs do not retarget it.
Their executors require that conversation; a future standalone operation must be
dispatched before that requirement. GitHub PR tools also narrow their installation
token to the assigned repository; standalone operations must choose their own
appropriate token scope. Adding a subscription does not assign a tool destination.

Subscriptions connect a conversation to an agent. An optional
`Definition.Subscription *SubscriptionDefinition` declares its provider and internal
forwarded `Events`; `Prepare(conversation)` validates the address and returns a
`Scope`. `Definition.Forwards(event)` applies that integration-owned policy.
Public catalog entries expose only `capabilities.subscription.conversation_schema`.
Attachments identify the integration and conversation; stored subscriptions retain
routing identity and lifecycle. Forwarding policy lives in the integration.
Absence of the capability rejects attachment.
Tool removal, config changes and posting do not alter subscriptions.
Launch attachments commit with the agent and initial input. Deleting a subscription
stops forwarding but retains launch ownership, so the next comment cannot launch
a replacement for a stopped conversation.

The shipped interaction handlers resolve the assigned conversation from integration
state and accept empty arguments. Shared storage looks up its existing target;
selection does not create targets or grants. In automatic mode, the last eligible content
input admitted to a turn selects its handler; dashboard/API inputs and integration origins
without an eligible handler clear that selection. Originless content from Omnara actors
with a valid agent or cron-trigger public ID preserves selection. Explicit integration
origins take precedence, including scheduled thread launches. Classify the stored actor
identity with `publicid.Decode`, never names, metadata or ID prefixes. Receipt alone does
not retarget a running turn. The model can select a destination and pin it with `auto_select: false`;
omitting that option preserves the current mode. Each question or approval captures
its destination. Callback owner IDs only route the request: verify that
owner's signature, live setup, current project credential access and captured
conversation before resolving it. Handler and profile-choice mutations hold the
credential's shared lock through commit and recheck project access after acquiring it.
Revocation disables external buttons; ordinary credential rotation preserves them.
Dashboard/API resolution does not depend on integration credentials.
Presentation failures leave the interaction available through the dashboard/API.
Notifications use the nonblocking background runner after creation commits, with
bounded in-memory retries. A full queue or restart can lose the external notification;
there is no durable presentation queue. Confirmed receipts support callbacks and
dismissal, including sends that finish after cancellation.

Use `executionstore.IntegrationActorParams` with the verified integration record for sender
attribution. `Definition.ActorIdentity` owns the stable platform namespace: Slack workspace,
Discord, or GitHub.com, paired with the platform user ID and scoped to the project. Different
configured bots and integration kinds on the same platform share person identity; do not
merge people across projects or platforms. Actors use `integration`, with a saved
`metadata.source_label` for display and no integration foreign key. Identity and labels
survive integration deletion. Authentication belongs to verified intake, not actor metadata.

## Events, retries and state

Verify and persist provider bytes before acknowledging ordinary webhooks. Each
saved integration verifies independently; partial fanout must not acknowledge an unknown
credential/database failure. Disconnect or repair a broken setup rather than
borrowing a sibling integration's credentials. GitHub requires explicit redelivery for
failures before durable intake; see the [operational guide](../../docs/integrations/github.mdx).
Signature verification and receipt insertion do not form an atomic credential
rotation fence: a just-replaced key can still admit an already-verified request.

Define the complete settings JSON schema with optional `Definition.Settings`, and
launcher matching/authorization with `Definition.Launcher`. The kernel stores JSON;
it does not inspect profiles, triggers or scope fields. Follow the schedule schema
pattern. `Matches` covers event and scope; `AuthorizeIntent` rechecks the exact
resource and internal launch key at both planning passes. An absent launcher
capability cannot authorize work. Set `SubscribeOnLaunch` independently.

The current chat helpers take ordered distinct public profile IDs, offer exactly
one selection, and resolve references at runtime. GitHub takes one profile and a
trigger. No setup profile locks or reverse deletion guard exist. Core still enforces
project/principal access, live resources, atomic admission and semantic idempotency.
Missing profiles must not silently narrow a menu; report unavailable. Keep the
internal launch key stable (`default` or `scheduled`), independent of profile order.
Only true saved launch ownership suppresses a launcher; receive-only subscriptions
must not. Shipped helpers are optional implementation reuse, not a mandatory settings
contract for future integrations.

Normalize provider events outside database transactions. Launcher policy decides
which profiles or agents to select; the router freezes those decisions and config
identities before provider preparation. Storage atomically commits each recipient's
agent/input, subscriptions and artifacts. Retry and completion checks read those
durable records; the inbox does not duplicate their outcomes. Unavailable-launch
feedback is best effort only after a newly committed matching plan, never during
retryable decision-making. Provider and blob I/O never run
under those locks. Preserve semantic event IDs separately from delivery IDs.

Forwarding policy is applied during planning, including prefiltering, launcher
selection, both freeze passes and matching subscriptions created in the same batch.
Frozen plans, including empty plans, keep those decisions on retry; unplanned
receipts use the current implementation policy. Live subscription removal,
integration disconnection and agent archival still revoke undelivered work. Storage
checks routing membership and ownership, without duplicating event policy or
persisting policy snapshots. The shipped Slack/Discord policies forward messages;
GitHub forwards comments, reviews and commits, while PR-open is launch-only.

Retries reuse frozen membership and artifact identities. Upload readiness is checked
within each attempt; incomplete file deliveries can verify previously uploaded
bytes again. Already-delivered work bypasses provider and upload preparation. Failed receipts
are terminal after the bounded budget: unfinished reservations release, committed
work remains, and old choice buttons cannot restart failed launches. Cleanup must
retain chooser source identity while its linked receipt exists. Incoming messages
are not strictly FIFO. Operational timing, retention and metrics belong in
[self-hosting configuration](../../docs/self-hosting/configuration.mdx#integration-inbox-operation).

Failure notices and cleanup of unreferenced prepared uploads are best effort after
the terminal commit. Crashes or lease recovery can skip them; retained database
receipts are not a general blob garbage collector. An archived recipient with no delivered input
is settled without delivery. Once the receipt is terminal, its unique planned
artifact IDs cannot be admitted by another attempt. Cleanup checks actual artifact
rows so delivered files, including archived history, remain intact.

Use `integration_states` for small workflow records keyed by integration, kind and key, optionally
indexed by conversation. A launcher can need state before an agent exists. Define
and validate JSON in its owning workflow; keep integration identity, credentials,
subscription routing and transport leases in their existing structures.
Replacements use revisions, and decisions with a deadline check database time at
the write. Decision deadlines are not retention TTLs. The profile chooser is the
existing example. A state transition can atomically enqueue work referencing that record through
`integration_state_id`. Register its `IntegrationStateHandler` by integration kind. The handler
interprets the state and chooses its retry policy; it completes through the supplied
`process(event, payload)` callback, using `process(nil, nil)` when no delivery is needed.
The inbox does not decode workflow-specific JSON.

State work can reserve a conversation while its plan is pending. This makes incoming
follow-ups and launches wait for that work to plan or finish. The producer must
serialize creation under the conversation lock and keep at most one active state-work
reservation for that conversation; competing reservations would wait on each other.

A launcher's optional pure `MayLaunchWithoutSelection` predicate protects early
replies once the provider routes the event. Provider I/O before routing is unprotected;
route before I/O where the payload supplies enough information. Single-profile chat
launchers opt in; menus and GitHub do not. The provider receipt saves its address in
the same reservation fields, but this marker only delays later provider receipts
that are planning delivery.
Actual launches and state work ignore provider markers, preserving first-freeze
ordering and accepted-choice priority. Accepted state reservations and frozen launch
claims still block at any age. Provider delivery waits only on earlier markers, so
receipts that become menu-only or delivery-only cannot wait on each other cyclically.
Markers survive retries and stop counting once their receipt has a plan or is terminal;
there is no explicit release step or separate pre-selection message buffer.

Query within an indexed identity/scope before filtering workflow JSON. Keep state
and retained histories small; JSON replacement rewrites the document. Do not infer
absence by filtering a limited page in Go, or add generic JSON indexes speculatively.
Typed replacement codecs must reject unknown fields or preserve them explicitly.

## Schedules and provider differences

Integrations publish schedule settings and validation; cron owns timing and durable
handoff. Register scheduled handlers by integration kind. Form hints such as
`x-omnara-control` only affect presentation; the JSON editor remains available.
References in settings resolve when the integration handles an occurrence. Retries then
use the frozen plan. Disabling a schedule stops future firings, not accepted work.

Slack/Discord schedules publish an opening and freeze its thread before launching.
A crash between publication and saving the plan can leave an unused opening and
permit a replacement. An early Slack reply can also precede the reservation.
There is no transaction across provider publication and PostgreSQL; do not promise
exactly-once posting. A completed receipt means launch succeeded, not that the
agent's eventual report was delivered.

Slack emits both message and mention callbacks. Preserve the sibling admission
rules: files arriving later may add one supplemental input without repeating the
text or canceling a newer interaction. Only the mention callback owns mention
failure feedback.

Discord creates a source message's thread only after freezing an authorized,
nonempty recipient plan. Thread replies require the exact subscription. Durable
Gateway checkpoints, shared bot IDENTIFY limits and signed callbacks are covered
in the [Discord protocol notes](discord/README.md).

GitHub tools stay within the assigned repository ID and PR. Human comments steer
and cancel interactions; commits queue. Repository checkout is separate. Normalize
from signed body fields: event/delivery headers are not covered by the HMAC.
Setup discovers bot identity rather than inferring it from App ID. Setup tokens
are separate from repository-scoped tool tokens. See GitHub's
[signature contract](https://docs.github.com/en/webhooks/using-webhooks/validating-webhook-deliveries)
and [App bot identity example](https://github.com/actions/create-github-app-token#configure-git-cli-for-an-apps-bot-user).

## Validation

Test a provider journey through verified intake, routing, admission and reply,
including replay, revoked credentials and project isolation. Use local HTTP and
WebSocket fixtures. Existing examples are `integration_slack_integration_test.go`,
`integration_discord_integration_test.go`, `integration_profile_choice_integration_test.go`, and
`internal/httpapi/github_event_routes_integration_test.go`.

Use the repository’s [validation and generation workflow](../../CONTRIBUTING.md).
