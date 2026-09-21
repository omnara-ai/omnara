# Contributing an Omnara app

Omnara hosts its built-in apps. Add an implementation through a normal code PR;
there is no runtime plugin loader or external registration protocol.
Customer-hosted integrations use ordinary inputs, custom/MCP tools and interaction
APIs, described in [Custom integrations](../../docs/integrations/custom-integrations.mdx).

## App identity and capabilities

A saved `ProjectAppRecord` owns its immutable name and app type, verified provider
identity, credential reference, setup revision, state and optional launcher.
Create the metadata first, then explicitly configure that app. Setup verifies
`ExpectedSetupRevision`, the observed credential version and current project
secret access. OAuth also targets that exact app/setup attempt. Reconnect can
replace credentials for the same provider identity; changing the identity needs
another app. Metadata/settings edits do not advance `SetupRevision` or restart a
provider session. Disconnect preserves recoverable work; delete tombstones the app.

Multiple saved apps may use the same physical bot, including across projects.
Each keeps its own setup, credential checks, inbox, subscriptions and runtime leases.
There is no global identity deduplication or credential-based app rediscovery.
Shared provider HTTP endpoints route to those independent apps.

Agent source attaches capabilities independently. For an app named `support`:

```yaml
tools:
  app__support__read: {}
  app__support__post_message: {}
interaction_handlers:
  support: {}
```

Compilation resolves the immutable name to a public project-app ID. Runtime lookup
uses that pinned app's registered type; name reuse cannot retarget it.
Tool schemas are static. Hosted launch admission saves one immutable sending
conversation per agent/app on its target. Calls can omit that destination or
supply matching fields; a channel context permits threads inside that channel,
while a thread context remains confined to that thread. Without context, callers
supply a complete address. Provider credentials remain the access boundary,
with ordinary tool permissions, project grants and provider checks at execution.

A tool grants no receive authority. App-owned subscriptions connect agents to a
named subscription type, one concrete `conversation` and resolved `events`.
Create them through the app-scoped subscription API or launch attachments; hosted
launchers attach their conversation atomically. An explicitly requested
`follow_replies` attaches a subscription only after an authorized, confirmed post.
No receive grant belongs in config. Removing tools or changing config preserves
subscriptions; deleting a subscription stops its forwarding. Interaction handlers
remain separate optional capabilities with complete runtime destination arguments,
independent of the sending context.

## Sender attribution

Construct hosted senders with `executionstore.AppActorParams(app.ID, senderID,
displayName)`. All providers use `provider=app`; `provider_tenant_id` holds the
configured app's public ID. Use those same values for actor-name lookups and
updates. Provider IDs/workspaces remain on the app record, not in the actor type.
Actors carry attribution, including approval responders; callback verification and
project authorization are owned by ingress. Historical Slack actors remain readable
unchanged. App deletion does not remove actor history. Scheduled tasks retain
Omnara cron attribution, and profile choices preserve the original message sender.

## Reuse an existing provider

Use an existing definition when its tools, subscriptions and launcher triggers
express the behavior. Slack and Discord use `slack_thread` and `discord_thread`; GitHub
mention and PR-open launchers use `github_pr`. Different profiles or destinations
normally need saved apps/config examples and journey tests, not another endpoint,
table, scheduler or app definition.

For a new operation, extend the definition's exported tool list in
`internal/appdefinition` and the schema/argument preparation in
`internal/toolcatalog/app_tools.go`. Add its provider execution in
`internal/harness/tools/<provider>_tools.go`. Qualified tools dispatch through
`internal/harness/tools/app_tools.go`; there is no global provider-tool alias.
Reuse `resolveAppToolAccess` and `recheckAppToolAccess` so the original proposed
config, current config, active app, setup revision and credential version all
remain part of execution authority. Approval descriptions must show the effective
destination even when the call relies on saved conversation context.

For new incoming events, extend normalization in `<provider>_event.go` (Slack uses
`app_slack.go`) and the definition's event/subscription contracts. Launcher trigger
validation belongs with saved-app settings and the reviewed definition. Keep
provider I/O outside router and database transactions. The code registry supplies
the public catalog and config schemas; do not create per-capability catalog rows.

## App-owned state

Use `app_states` for small independent workflow records. Each belongs to a
project/app and has an immutable `kind` plus `key`; optional scope columns group
related records, such as requests in one conversation. There is no required
agent: a launcher may need state before an agent exists. Avoid one growing
document for an entire app. The 2 MiB document ceiling is a supported maximum,
not a working-size target. Keep subscriptions and transport leases in their
existing stores.

Look up records by indexed identity or scope. Workflow SQL can filter its own
JSON fields within that scope, before applying a limit. This assumes small
retained histories per scope, including expired or failed records. A JSON
predicate can fetch the whole document, and replacements rewrite it; assess
these costs when a workflow needs large records or searches across many scopes.
Do not add shared indexes on app-specific JSON fields or fetch a limited page
and infer that no eligible work exists from Go filtering.

Define and validate the document in the owning Go workflow. The profile chooser's
codec and semantic methods in `internal/storage/integrationstore/app_profile_choices*`
are an example. Compose the shared queries from `app_states.sql` within that
store's transaction, after the appropriate lifecycle and conversation gates.
Keep generated query types inside the store. Replacements use an expected
revision; a decision requiring a live deadline must check it at the SQL write.
Do not perform provider I/O while holding the transaction.

The chooser rejects unknown document fields because typed replacements would
otherwise discard them silently. Keep retained documents readable when changing
their schema, and coordinate readers and writers with the cluster release or an
explicit data migration. A revision is a mutation counter, not a schema version.

A deadline is not an automatic deletion policy. Reads retain expired data;
workflow-specific retention must preserve recovery. `CleanupAppStates` first
reclaims records for deleted owners, then spends its remaining deletion budget
on chooser expiry. The chooser's expiry query and inbox recovery guard stay in
`app_profile_choices.sql`; generic deleted-owner cleanup lives in `app_states.sql`.
Add another workflow's retention policy only when its actual lifecycle requires it.

## Add a provider

| Responsibility | Location |
| --- | --- |
| Exported tools, named subscription types, optional handler and concrete addresses | `internal/appdefinition` |
| HTTP/socket clients, signatures, protocol validation and bounded retries | `internal/integration/<provider>/` |
| Verified receipt expansion into ordinary agent input | `internal/integration/<provider>_event.go` |
| Tool schemas and qualified execution | `internal/toolcatalog`, `internal/harness/tools` |
| App setup, verified intake and worker registration | `internal/httpapi`, `cmd/worker` |
| App identity, subscriptions, app state, inbox and runtime leases | `internal/storage/integrationstore` |
| Atomic agent launch/input admission and interaction resolution | `internal/storage/executionstore` |

Register a new app type in `internal/appdefinition`, add its PostgreSQL
`project_apps.app_type` constraint value and OpenAPI `AppType` enum value, and
regenerate contracts. Use `slack_thread`, `discord_thread` and `github_pr` as
examples. Saved apps store only that type; the registry declares the transport
used by its implementation. Discovery derives the transport's supported types
with `AppTypesForProvider`, so two app types can reuse one provider without
another stored classification. Add setup and provider clients only when needed.
Provider packages own protocol details; they do not own agent launches, durable
routing or product transactions.

Ordinary webhook intake pages active apps by provider identity. Each verifies its
own credentials and durably accepts its app-local receipt before success. Known
invalid signatures and revoked or missing setups are skipped. Unknown active-app secret,
decryption/KMS or database errors return non-2xx even when healthy siblings have
committed; redelivery reuses each app's receipt key. A persistently corrupt setup
therefore remains retryable until an operator reconnects that app with valid
credentials or disconnects it. Failure logs identify the affected app/project
without secret values, payloads or raw error text. See the
[cutover recovery runbook](../../docs/self-hosting/composable-apps-cutover.mdx#shared-webhook-setup-failures).

Slack also checks disconnected, non-deleted apps with retained credentials so
their valid signatures can receive `200 ignored`, without inbox or lifecycle
mutations. Each candidate verifies independently. An unavailable disconnected
credential is logged but does not block an independently verified sibling's
acknowledgement; if nothing verifies and an unknown error occurred, intake returns
503. Active-app errors still require retry even when a disconnected app verifies.

Callback buttons and profile menus instead resolve one captured owner by ID, then
verify that exact app's signature, receipt, destination and live setup revision.
Unauthenticated owner lookup is routing metadata only.

Register `AppInboxProvider` and `AppLaunchWorkflow` implementations in `cmd/worker`.
Slack/Discord also implement the narrow scheduled conversation provider in
`app_scheduled.go`: publish a root, freeze its launch plan, then ensure its thread.
The existing cron app-launch target and inbox own scheduling and recovery; do not
add another scheduler or mutate tool configs after launch.
Slack/Discord's `ChatAppLauncher` directly selects a sole profile or presents a
choice; GitHub's `EverySlotAppLauncher` explicitly selects its configured slots.
The router freezes those decisions and pinned configs, then executionstore admits
each slot atomically. See [app_routing.md](app_routing.md) for replay and leases.

A persistent transport retains its existing app/shard lease and checkpoint
contract independently of receipt consumption. Discord saved apps may open
independent Gateway sessions for the same physical bot. Their IDENTIFY budget
and concurrency buckets still coordinate by bot; unrelated launcher/settings
edits must not bounce those sessions.

Implement `InteractionPresenter` support only when the provider can present and
answer existing forms. Extend delivery discovery and provider presentation,
dismissal and callback handling. Capture handler key, app ID, arguments
and the concrete destination. Handler discovery alone creates no target;
selection/presentation uses current authority. Failed presentation leaves the
core interaction answerable through dashboard/API. GitHub intentionally has no
interaction presenter.

## Trace and test a journey

A Slack mention launches into its thread; later messages use its app-owned
`thread_messages` subscription. `app__support__post_message` can send proactively
and explicitly request `follow_replies` without any receive capability in config.
Verified inputs include the app name and actual channel/thread IDs so flexible
tools can address the conversation.
Discord follows the same pattern with leased Gateway intake and bounded thread
preparation; see [discord_event.md](discord_event.md) and
[discord/README.md](discord/README.md).

A GitHub mention or PR-open event selects the configured launcher. The derived
config supplies its tools, while admission saves the repository/PR as sending
context. Its separate `pull_request` subscription freezes the conversation and
definition default events.
Human comments steer/cancel open interactions; commits queue. Inline comment IDs
and diff coordinates stay in tools. Repository checkout remains machine/profile
setup. See [github_event.md](github_event.md).

Test the changed boundary: protocol/normalization, context-bound and explicit-destination tools,
subscription attachment, deletion and event filtering, captured callbacks, and
inbox-to-agent replay. Include independent apps sharing a physical identity,
cross-project isolation, revoked
credentials, setup races, partial fanout/launch recovery, and uncertain sends.
Use local HTTP/WebSocket fixtures; live tests are a separate check.

Examples include `app_slack_integration_test.go`, `app_discord_integration_test.go`,
`app_overlap_integration_test.go`, `app_profile_choice_integration_test.go`,
`internal/httpapi/github_event_routes_integration_test.go`,
`internal/httpapi/captured_interaction_routes_integration_test.go`, and provider
tool tests in `internal/harness/tools`.

From the repository root, run focused tests first, then `make verify`. Storage
journeys use the local database stack and `go test -tags=integration`. Regenerate
SQL with `make sqlc-generate`; public contracts with
`make openapi-generate docs-openapi web-generate`. OpenAPI generation also refreshes
the agent config JSON schema and its shared OpenAPI definitions from their source.
Keep generated output with its source. See the
[definition guide](../appdefinition/README.md) for config validation checks.
