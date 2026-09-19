# Contributing an Omnara app

Omnara hosts its built-in apps. Add an implementation through a normal code PR;
there is no runtime plugin loader or external app registration protocol.
Customer-hosted integrations use ordinary inputs, custom/MCP tools and interaction
APIs, described in [Custom integrations](../../docs/integrations/custom-integrations.mdx).

Start with the behavior, not a new provider. Two Slack bots with different
profiles, destinations, tools or listeners can share one provider connection and
webhook endpoint when they use the same Slack bot account. Distinct bot identities
need distinct connections. Saved project apps supply those settings. A listener and a tool
are independent: receiving an event does not grant permission to post, and posting
does not subscribe the agent unless an authorized follow policy is requested.

## Reuse an existing provider

If existing tools, event kinds and launcher triggers express the behavior, add a
config/setup example and its journey test. No new definition ID, endpoint, table
or scheduler is needed. For example, a Slack thread assistant and a channel
notification agent select different scopes and capabilities from `omnara.slack`.
GitHub's mention and PR-open launch choices similarly reuse `omnara.github`.

For a new operation, add its schema, provider and follow capability to the single
definition table in `internal/toolcatalog/app_tools.go`, and its handler to
`internal/harness/tools/<provider>_tools.go`, using the existing provider client.
Register the handler in the provider's `*ToolRegistrations` function. Reuse
`resolveAppToolAccess` and `recheckAppToolAccess`: an app config grants a scope,
not unrestricted access to the underlying credentials. Tool permissions use the
ordinary harness permission machinery. To expose the operation in console/CLI
setup, also add it to `profileAppTools` in
`frontend/packages/sdk/src/profile-app.ts`; this explicit list is shared by both
setup clients.

For a new incoming event, normalize it in `<provider>_event.go` (Slack currently
uses `app_slack.go`), extend the pure event/selection rules in
`internal/appdefinition`, and test the behavior through the common router. A new
launcher trigger also needs validation in `integrationstore/project_apps.go` and
a setup choice in `frontend/packages/sdk/src/profile-app.ts` when offered there. Keep
provider I/O out of the router and database transactions. If behavior needs a new
reviewed definition, register its stable ID and capabilities in `appdefinition`;
the provider connection and intake still remain shared.

## Add a provider

These are the extension boundaries, not a requirement to edit every file for
every app:

| Responsibility | Location | Example |
| --- | --- | --- |
| Typed scope, event kinds, launcher addresses and definition metadata | `internal/appdefinition` | GitHub PR scope; Slack channel/thread containment |
| Provider HTTP/socket client, verification and bounded retries | `internal/integration/<provider>/` | `github/`, `discord/`, `slack/` |
| Verified receipt expansion into ordinary agent input | `internal/integration/<provider>_event.go` | `GitHubAppInboxProvider`, `DiscordAppInboxProvider` |
| Tool schema and scoped execution | `internal/toolcatalog`, `internal/harness/tools` | `github_read`, `slack_post_message`, `discord_post_message` |
| Setup, authenticated intake and worker wiring | `internal/httpapi`, `cmd/worker` | `github_connection.go`, `github_event_routes.go`, the `AppInboxProvider` map |

A new provider also extends the closed provider domain in storage, migration and
OpenAPI, its typed config scope/schema, and setup UI/CLI where supported. Update
those owners explicitly and regenerate contracts; don't hide them behind an
unvalidated string or open JSON configuration. An additional behavior using an
existing provider normally needs no schema migration.

Provider packages own provider protocol details, not agent launches or storage.
HTTP handlers verify the callback and persist an inbox receipt before reporting
acceptance for routed work. `AppInboxProvider.Expand` translates that receipt into
`AppEvent` values with a stable semantic key, verified actor, scope, input content
and delivery/cancellation policy. Transport delivery IDs and semantic message IDs
can differ; Slack's duplicate event shapes demonstrate why this matters.

App launchers are registered explicitly in `cmd/worker`. `AppLaunchWorkflow`
runs the chosen app policy before config derivation or agent admission. It may
return no launch, one `AppLaunchIntent`, or several. Our Slack and Discord
`ChatAppLauncher` offers one profile or a menu; GitHub explicitly uses
`EverySlotAppLauncher`. A contributor's app can choose another policy. Do not put
chooser state in an agent interaction or hard-code fan-out in the shared router.

The shared `AppRouter` validates those explicit intents and resolves listeners,
pins launch configs, and freezes a retryable plan. `executionstore` commits agents, selected listeners
and inputs atomically. `integrationstore` owns connections, saved apps and inbox
lifecycle. New provider code should not reproduce these transactions or add its
own agent-to-conversation table. See [app_routing.md](app_routing.md) for recovery
and authority details.

Register the receipt adapter in the worker's provider map and append the provider's
tool registrations in `builtInToolRegistrations` in
`internal/harness/tools/registry.go`. If the provider needs
a persistent connection, own its lifecycle separately from receipt processing;
Discord's leased Gateway runtime is the current example. A second Discord app
behavior must reuse that transport rather than opening a duplicate connection.

An interaction handler is optional. Implement one only when the provider can
present and answer the existing form. Use `InteractionPresenter`, register its
handler in the discovery list in `interaction_delivery.go`, extend the access,
presentation and dismissal switches in `interaction_presenter.go`, and add
verified callbacks with captured destination and current authority checks. Failed
presentation leaves the dashboard/API usable. GitHub deliberately has no approval
presenter; its PR comments remain ordinary tools.

## Trace the examples

**Slack thread assistant.** Verified Slack intake captures a receipt. A mention
selects a project app/profile; `SlackAppInboxProvider` normalizes the message and
the shared router launches a thread-scoped agent. Later thread replies go through
its listener. `slack_post_message` posts using that scope. A channel-scoped
proactive sender can request `follow_replies`, which registers only the confirmed
conversation under the existing follow policy.

**Discord thread assistant.** The leased Gateway runtime persists verified
messages. `DiscordAppInboxProvider` normalizes mentions and replies. Thread
creation normally uses the provider's `PrepareConversation` step, after a nonempty
plan is frozen and after checking live authority. A multi-profile launcher creates
the request thread first to display its menu; the chosen launch reuses that thread.
The router then admits the agent/input.
`discord_post_message` and the follow policy cover proactive conversations.
See [discord_event.md](discord_event.md) and [discord/README.md](discord/README.md).

**GitHub PR reviewer.** `github_event_routes.go` validates a signed callback for
its installation and persists a receipt. `GitHubAppInboxProvider` normalizes PR
and comment events. The saved launcher chooses mention or PR-open behavior. The
agent gets PR-scoped read/comment/reply tools and selected PR listeners. Inline
comment IDs and diff coordinates stay in the GitHub tools; they are not separate
Omnara channels. Human comments steer/cancel open interactions, while commits
queue. Reading repository contents or checking out code is separate machine/profile
setup. See [github_event.md](github_event.md) and [github/doc.go](github/doc.go).

## Prove the journey

Keep provider fixtures beside their implementation and add coverage at the
boundary actually changed: normalization/signature tests, scoped tool tests, and
an inbox-to-agent journey with replay and continuation. Important failures include
cross-project or out-of-scope access, revoked connections, duplicate delivery,
bot self-events, partial launch recovery and uncertain sends. Add a callback test
if the app supports approvals. Use HTTP/socket fixtures for routine tests; a live
provider test is a separate check, not the only evidence.

Existing examples are `app_slack_integration_test.go`,
`app_discord_integration_test.go`, `internal/httpapi/github_event_routes_integration_test.go`
and the provider tool integration tests in `internal/harness/tools`.
`app_profile_choice_integration_test.go` covers the app-owned chooser, original
request preservation, restart, overlapping setups and delayed attachments. The ordinary
customer-owned API journey is covered separately from hosted app routing.

From the repository root, run focused tests first, then `make verify`. For changes
to storage, use the local database stack and `go test -tags=integration` on the
affected packages. Run `make sqlc-generate` after SQL changes, and
`make openapi-generate docs-openapi web-generate` after public schema changes.
For config schema changes, first regenerate the config schema with
`OMNARA_REGEN_AGENT_CONFIG_SCHEMA=1 go test ./internal/agentconfig -run TestGeneratedAgentConfigSourceSchemaIsCurrent`,
then run the OpenAPI/docs/web commands. See the
[definition guide](../appdefinition/README.md) for config validation checks.
Commit generated output with its source. Add setup documentation showing
the provider credentials, minimal config and expected conversation behavior.
