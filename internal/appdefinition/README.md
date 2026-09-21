# Built-in app contributions

Apps are reviewed code contributions shipped with Omnara. `Lookup` in
[`definition.go`](definition.go) describes Slack, GitHub and Discord. Saved project
apps own setup and credentials and reference one immutable `app_type`. This
package contains pure metadata, concrete address validation and provider addresses;
it performs no storage reads or provider I/O.

`Type` identifies the registered implementation: `slack_thread`, `discord_thread`
or `github_pr`. PostgreSQL and OpenAPI enforce the same closed set. `Definition`
keeps the transport association in code; saved app rows do not store a provider
classification. `AppTypesForProvider` supplies indexed ingress/runtime discovery
without inferring a transport from the type's name. Compiled agent capabilities
still pin only the app instance ID.

A definition exports operation names, named `SubscriptionDefinition` entries and at
most one `InteractionHandlerDefinition`. GitHub has no interaction handler.
Tools and handlers remain independent of subscriptions. Launchers are configured
separately in project setup and attach subscriptions during admission.

Provider addresses use `SlackScope`, `GitHubScope` and `DiscordScope` inside a
`Scope`. `DestinationProperties(provider)` returns static address fields and the
required fields for a complete destination. `ResolveDestination(provider, args)`
validates that complete, closed address. Slack and Discord require `channel_id`;
GitHub requires `repository_id` and `pull_request`. Discord `guild_id` is optional
metadata; provider execution verifies it when supplied. Scheduled launches keep
concrete destination setup in app/trigger config and accept only parent channels.

The shipped tools have static action-only schemas. At execution the harness
loads the immutable conversation assigned to the agent/app at launch. Missing
context fails before provider I/O. Tools do not accept addresses, infer sending
authority from subscriptions or alter subscriptions when posting.

Subscriptions take one flat provider `conversation` object and optional top-level
`events`. `SubscriptionDefinition.ConversationSchema()` describes the address;
`Prepare(conversation, events)` validates it and returns `PreparedSubscription`
with a concrete `Scope` and sorted events. Omitted events select all supported
events. Empty, duplicate and unknown event selections are rejected, as are empty
or multiple conversations. The exported names are `thread_messages` for
Slack/Discord and `pull_request` for GitHub.

`Scope.Conversation()` yields the canonical routing kind/key. `ConversationJSON()`
encodes the flat provider address, and `ParseConversation(provider, kind, ref)`
reconstructs it from indexed columns. Discord guild IDs are optional metadata and
are not recoverable from the channel/thread key. Parent launcher scopes are not
subscription conversations. Verified event routing and launcher matching remain
in `events.go` and `launcher.go`.

Storage owns app/agent scope checks, event-filter conflicts, quota and atomic
launch attachments. Subscriptions live independently of agent config: changing
or removing tools and handlers never reconciles routes. Detach removes one
immutable subscription ID; a fresh attachment gets a new ID. App disconnection
suspends delivery without deleting routes.

Interaction handlers are independent of sending context. `Prepare()` exposes a
static complete-address schema; `ResolveArgs(args)` validates the selected
address. Verified input origins provide full arguments through
`Scope.ConversationJSON()`. Storage/runtime own persistent selection, prompt
capture and authenticated callbacks; the dashboard remains available.

Operation schemas and implementations of the pure tool preparation contract live
in `internal/toolcatalog`. Transports belong to `internal/integration`; provider
execution belongs to `internal/harness`. Keep standalone custom tools and MCP
independent. The compiler/caller contract is in
[`../agentconfig/apps.md`](../agentconfig/apps.md).

Run the focused checks after a contribution:

```sh
make openapi-generate
go test -race ./internal/appdefinition ./internal/toolcatalog ./internal/agentconfig
```
