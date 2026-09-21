# Built-in app contributions

Apps are reviewed code contributions shipped with Omnara. `Lookup` in
[`definition.go`](definition.go) describes Slack, GitHub and Discord. Saved project
apps own setup and credentials and reference one immutable definition. This
package contains pure metadata, concrete address validation and provider addresses;
it performs no storage reads or provider I/O.

A definition exports operation names, named `SubscriptionDefinition` entries and at
most one `InteractionHandlerDefinition`. GitHub has no interaction handler.
Tools and handlers remain independent; explicit `follow_replies` on an authorized
post can establish an app-owned subscription after confirmed publication. Launchers are configured separately in project setup.

Provider addresses use `SlackScope`, `GitHubScope` and `DiscordScope` inside a
`Scope`. `DestinationProperties(provider)` returns static address fields and the
required fields for a complete destination. `ResolveDestination(provider, args)`
validates that complete, closed address. Slack and Discord require `channel_id`;
GitHub requires `repository_id` and `pull_request`. Discord `guild_id` is optional
metadata; provider execution verifies it when supplied. Scheduled launches keep
concrete destination setup in app/trigger config and accept only parent channels.

Tools have static schemas with optional destination arguments. At execution,
`toolcatalog.AppToolDefinition.ResolveArgs(raw, context)` uses an immutable
conversation context when present: omitted fields inherit the context and
supplied fields must stay within it. A Slack channel/DM or Discord server channel context permits
threads inside that channel; a thread context remains confined to that thread.
GitHub context remains exact to its repository and pull request. Without context,
arguments must supply a complete destination. Indexed Discord context lacks guild
metadata, so a supplied guild remains available for live provider verification.

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

Storage owns app/agent scope checks, event-filter conflicts, quota, atomic launch
attachments and confirmed-send registration. Subscriptions live independently of
agent config: changing or removing tools and handlers never reconciles routes.
Detach removes one immutable subscription ID; a fresh attachment gets a new ID.
Completed tool replay cannot restore a detached route. A confirmed in-flight
post may establish a subscription after detach while preserving its original
sender provenance. App disconnection suspends delivery without deleting routes.

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
OMNARA_REGEN_AGENT_CONFIG_SCHEMA=1 go test ./internal/agentconfig -run TestGeneratedAgentConfigSourceSchemaIsCurrent
go test -race ./internal/appdefinition ./internal/toolcatalog ./internal/agentconfig
```
