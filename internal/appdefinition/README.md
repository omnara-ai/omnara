# Built-in app contributions

Apps are reviewed code contributions shipped with Omnara. `Lookup` in
[`definition.go`](definition.go) describes Slack, GitHub and Discord. Saved project
apps own setup and credentials and reference one immutable definition. This
package contains pure metadata, typed config validation and provider addresses;
it performs no storage reads or provider I/O.

A definition exports operation names, named `SubscriptionDefinition` entries and at
most one `InteractionHandlerDefinition`. GitHub has no interaction handler.
Tools and handlers remain independent; explicit `follow_replies` on an authorized
post can establish an app-owned subscription after confirmed publication. Launchers are configured separately in project setup.

Provider destination fields use `SlackConfig`, `GitHubConfig` and `DiscordConfig`.
All are optional hidden settings: omitted required destination fields remain
model arguments. `CanonicalDestinationConfig`, `DestinationArguments` and
`ResolveDestination` enforce the provider's closed typed contract. Fixed values
cannot be supplied again as model arguments. `DestinationDescription` formats
only validated provider IDs for model/UI/approval use. A fixed Slack `thread_ts`
or Discord `thread_id` requires a fixed `channel_id`; a fixed GitHub
`pull_request` requires `repository_id`. Parent-only configs remain valid.
Discord `guild_id` is optional even for a fixed channel/thread, and a guild-only
config leaves the channel and thread as arguments.

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

Interaction handlers prepare a safe description and effective destination schema.
`ResolveArgs` validates selection; `ArgsForDestination` derives arguments from
verified input origin, rejecting mismatches with fixed config. Storage/runtime
own persistent selection, prompt capture and authenticated callbacks.

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
