# Contributing an Omnara integration

Built-in integrations are reviewed code shipped with Omnara. Customer-hosted integrations
use the [public input, custom-tool and interaction APIs](../../docs/integrations/custom-integrations.mdx).
For user setup, see [Integrations](../../docs/integrations/overview.mdx); for deployment, see
[the Slack cutover guide](../../docs/self-hosting/composable-integrations-cutover.mdx).

## Where to make a change

| Responsibility | Owner |
| --- | --- |
| Integration type, capabilities, addresses and schedule schema | `internal/integrationdefinition` |
| Tool schemas | `internal/toolcatalog/integration_tools.go` |
| Config name resolution and pinned integration IDs | `internal/agentconfig`, `internal/agentconfigcompile` |
| Tool execution and credential checks | `internal/harness/tools/integration_tools.go`, `integration_authority.go` and provider tool files |
| Provider HTTP/socket protocol | `internal/integration/<provider>/` |
| Event normalization and launcher policy | Provider event files, `integration_launch.go` and `integration_profile_choice.go` |
| Setup and verified webhook/callback intake | `internal/httpapi` |
| Provider, launcher and scheduled-action registration | `cmd/worker` |
| Integration, subscription, workflow state, inbox and transport leases | `internal/storage/integrationstore` |
| Atomic agent launch/input and interaction resolution | `internal/storage/executionstore` |

Start with `slack_thread`, `discord_thread` or `github_pr`. Adding an operation to
an existing integration usually touches its definition, tool schema and provider executor.
A new integration type also needs the PostgreSQL constraint and OpenAPI enum updated,
then regenerated contracts. Keep provider classification in the code registry;
several integration types can share a transport without another database column.

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
stops forwarding but retains selection history, so the next comment cannot launch
a replacement for a stopped conversation.

Interaction destinations are independent of sending context. Accepted input
origins can select a handler; the model can change it. Each question or approval
captures its destination. Callback owner IDs only route the request: verify that
owner's signature, live setup and captured conversation before resolving it.
Presentation failures leave the interaction available through the dashboard/API.
Presentation claims are one-shot, so a crash can lose the external notification.

Use `executionstore.IntegrationActorParams` for sender attribution. Actors use `integration` and
the saved integration's public ID, without a foreign key that would erase history on
integration deletion. Authentication belongs to verified intake, not actor metadata.

## Events, retries and state

Verify and persist provider bytes before acknowledging ordinary webhooks. Each
saved integration verifies independently; partial fanout must not acknowledge an unknown
credential/database failure. Disconnect or repair a broken setup rather than
borrowing a sibling integration's credentials. GitHub requires explicit redelivery for
failures before durable intake; see the [operational guide](../../docs/integrations/github.mdx).
Signature verification and receipt insertion do not form an atomic credential
rotation fence: a just-replaced key can still admit an already-verified request.

Declare supported launch triggers and set `SubscribeOnLaunch` explicitly in the
integration definition. Subscription capability and automatic launch attachment are
independent choices; a capable integration may leave launch subscription disabled.
Launchers still add the integration's declared tools and handler, preserving
explicit config choices; tool scope does not change that composition policy.
Provider event matching and a custom launcher must agree with the declaration,
because early routing can discard events before the launcher runs.

Normalize provider events outside database transactions. Launcher policy decides
which profiles or agents to select; the router freezes those decisions and config
identities before provider preparation. Storage atomically commits each recipient's
agent/input, subscriptions, artifacts and progress. Provider and blob I/O never run
under those locks. Preserve semantic event IDs separately from delivery IDs.

Forwarding policy is applied during planning, including prefiltering, launcher
selection, both freeze passes and matching subscriptions created in the same batch.
Frozen plans, including empty plans, keep those decisions on retry; unplanned
receipts use the current implementation policy. Live subscription removal,
integration disconnection and agent archival still revoke undelivered work. Storage
checks routing membership and ownership, without duplicating event policy or
persisting policy snapshots. The shipped Slack/Discord policies forward messages;
GitHub forwards comments, reviews and commits, while PR-open is launch-only.

Retries reuse frozen membership and prepared artifact identities. Failed receipts
are terminal after the bounded budget: unfinished reservations release, committed
work remains, and old choice buttons cannot restart failed launches. Cleanup must
retain chooser source identity while its linked receipt exists. Incoming messages
are not strictly FIFO. Operational timing, retention and metrics belong in
[self-hosting configuration](../../docs/self-hosting/configuration.mdx#integration-inbox-operation).

Failure notices and cleanup of unreferenced prepared uploads are best effort after
the terminal commit. Crashes or lease recovery can skip them; retained database
receipts are not a general blob garbage collector. A committed archived-recipient
skip settles work without delivering it; its unused uploads can be cleaned after
receipt completion or failure. Cleanup must preserve delivered slots and durable
artifact references, including archived history.

Use `integration_states` for small workflow records keyed by integration, kind and key, optionally
indexed by conversation. A launcher can need state before an agent exists. Define
and validate JSON in its owning workflow; keep integration identity, credentials,
subscription routing and transport leases in their existing structures.
Replacements use revisions, and decisions with a deadline check database time at
the write. Decision deadlines are not retention TTLs. The profile chooser is the
existing example.

Query within an indexed identity/scope before filtering workflow JSON. Keep state
and retained histories small; JSON replacement rewrites the document. Do not infer
absence by filtering a limited page in Go, or add generic JSON indexes speculatively.
Typed replacement codecs must reject unknown fields or preserve them explicitly.

## Schedules and provider differences

Integrations publish schedule settings and validation; cron owns timing and durable
handoff. Register scheduled handlers by integration type. Form hints such as
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
