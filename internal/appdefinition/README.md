# Built-in app contributions

Apps are reviewed code contributions shipped with Omnara. `Lookup` in
[`definition.go`](definition.go) describes Slack, GitHub and Discord. Saved project
apps own setup and credentials and reference one immutable definition. This
package contains pure metadata, typed config validation and provider addresses;
it performs no storage reads or provider I/O.

A definition exports operation names, named `ListenerDefinition` entries and at
most one `InteractionHandlerDefinition`. GitHub has no interaction handler.
Capability selection is independent; enabling a tool grants no listener or
handler. Launchers are configured separately in project setup.

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

Listeners have provider-typed `conversations` and bounded `events` vocabularies.
`ListenerDefinition.Prepare` returns canonical config, initial `[]Scope` addresses
and current allowed events. Empty conversations mean no initial subscriptions;
omitted events select the listener's supported events. Storage separately owns
initial subscription activation, runtime follows, reconciliation and revocation.
`Scope.Conversation` provides canonical routing kind/key, not a generic ACL.
Verified event routing and launcher matching remain in `events.go` and `launcher.go`.

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
