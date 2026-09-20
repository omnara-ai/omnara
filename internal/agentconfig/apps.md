# App-owned config contract

App capabilities reference a saved project app by immutable name in source and
by public `app_…` ID in compiled configs. A project-scoped `ResolveAppName` returns
`AppResolution{AppID, Definition}`. The compiler resolves each distinct name once;
it persists only `AppID` and canonical validated config, never definition metadata
or credentials. Public IDs use `publicid.KindProjectApp`.

```yaml
tools:
  app__engineering__post_message:
    permission: {mode: always_ask}
    config: {channel_id: C123}
listeners:
  engineering__thread_messages: {}
interaction_handlers:
  engineering:
    config: {channel_id: C456}
```

Qualified tools need no `type` or operation field. Config is a hidden object,
separate from model arguments. All eight shipped operations accept `{}` and
expose their required destination arguments in that case. Fixed destination
fields are absent from the effective argument schema and cannot be overridden.
Fixed Slack/Discord thread fields require their channel; a fixed GitHub pull
request requires its repository. Discord channels and threads need no fixed guild.
Policy-only entries select that same flexible operation. Ordinary built-in and
custom tools accept empty config and reject unsupported nonempty config. Custom
and MCP schemas are not transformed; only `app__` and `mcp__` are reserved
prefixes. Ordinary custom names may contain `__`.

`AgentConfigSource.Listeners` and `.InteractionHandlers` are
`map[string]AgentConfigAppCapabilitySource`, whose only field is
`Config map[string]any`. Compiled maps contain
`AppCapabilityCompiled{AppID string, Config json.RawMessage}`. Listener keys are
`<app>__<listener>`; handler keys are the app name. Tools use
`ToolCompiled.AppID` and `.Config` alongside their existing enabled, permission
and deferred settings.

Shipped listeners are `thread_messages` for Slack/Discord and `pull_request` for
GitHub. `appdefinition.Lookup(definition).Listeners[name].Prepare(config)` returns
canonical config, allowed events, and initial `[]Scope` conversations. Omitted
events mean the listener's supported events. Empty conversations subscribe nowhere.
Each concrete scope's `Conversation()` returns the routing kind/key. Runtime
follows belong to the selected listener; removing a send tool cannot revoke them.
The send operation's `FollowListener` identifies the listener required before a
follow-enabled send. Activation, reconciliation and confirmed-send transactions
remain storage responsibilities; saving a config alone creates no subscriptions.

## Preparation and immutable authority

`RuntimeContractFromCompiled` validates and decodes without database I/O or new
default injection. It places ordinary tools in `.Tools`, pinned app tools in
`.AppTools`, and retains `.Listeners`/`.InteractionHandlers`. App tools are not
model-ready until preparation. `ReferencedAppIDs(compiled)` and
`contract.ReferencedAppIDs()` return the same sorted, distinct read set. Disabled
tools contribute no IDs; independently configured listeners or handlers still do.
Disabled entries remain stored and undergo the same strict name/AppID consistency
validation as enabled entries.

Callers load that read set once with project ownership and live status checks,
then call:

```go
PrepareAppCapabilities(compiled Compiled, apps map[string]AppResolution) (PreparedAppCapabilities, error)
```

`apps` is keyed by pinned public app ID; each value contains the same `AppID` and
its immutable `Definition`. The result contains effective `.Tools []RuntimeTool`,
prepared `.Listeners` and `.InteractionHandlers`, plus `.Unavailable` errors keyed
by capability JSON pointer. A missing/inactive app affects its own capabilities;
ordinary config and dashboard decoding still work. Merge prepared tools with the
ordinary runtime tools before model exposure. Credentials are resolved separately,
live, immediately before provider execution.

`toolcatalog.LookupAppTool(definition, operation)` returns provider metadata with
`ConfigSchema`, `CanonicalConfig`, `Prepare`, `Describe`, and `ResolveArgs`
methods. `Prepare(qualifiedName, config)` creates an effective catalog entry.
`ResolveArgs(config, args)` validates that same schema and returns
`AppToolArguments{Destination Scope, Arguments json.RawMessage, FollowReplies bool}`.
`Arguments` contains operation arguments; `Destination` contains the concrete,
validated address. `Describe` safely formats fixed IDs for model descriptions,
approval summaries and config UI.

Pending calls must pass:

```go
ResolveAppToolAuthority(original, current RuntimeContract, name string, apps map[string]AppResolution) (AppToolAuthority, error)
ResolveInteractionHandlerAuthority(original, current RuntimeContract, key string, apps map[string]AppResolution) (PreparedAppInteractionHandler, error)
```

Tool authority requires unchanged pinned app ID, canonical effective config and
permission, with the tool enabled and not denied in both configs. The result
contains the original canonical `.Tool` and its `.Definition`. Handler authority
requires the same app ID and effective config. Unrelated edits do not invalidate
a call. Reusing an app name cannot redirect a captured call or interaction.

## Composition and subagents

`AppCapabilitiesSource` has `Tools`, `Listeners`, and `InteractionHandlers` maps
with the same source entry types. The exported entry points are:

```go
CompileAppCapabilitiesSource(source AppCapabilitiesSource, opts CompileOptions) (Compiled, error)
DeriveWithAppCapabilities(base Compiled, additions AppCapabilitiesSource, opts CompileOptions) (Compiled, error)
```

The first compiles capabilities alone, without defaults or model/machine/skill
resolution. Derivation removes all existing keys before validating/resolving
additions. Existing disabled tools, permissions, empty listeners, and destinations
win completely; even malformed or unavailable redundant additions are ignored.
Base model/machine/skill identities remain pinned. Launcher admission separately
registers the actual launch conversation under the configured listener.

`SubagentCompiledFrom` removes all app tools, listeners and handlers. Ordinary
custom tools, MCP and built-ins remain independent. Runtime subscriptions are
never inherited. Resources, app provenance, bundles, provider-wide policies and
generic scope-containment authority have been removed.

## Interaction helpers

New compilations for tool-capable models include `list_interaction_handlers` and
`set_interaction_handler` regardless of configured handlers. Explicit enabled,
permission and deferred overrides win. Models explicitly lacking tool support
receive neither optional default; explicit enabled tools still require support.
Stored configs are never given these defaults during decoding.

The setter takes `{handler, args}`; `handler: null` with empty args means dashboard
only. The harness validates the outer tool contract; executionstore checks the
selected handler’s original/current authority and resolves its arguments before
persisting the selection. Runtime captures that selection on each prompt.

`ListInteractionHandlers(preparedHandlers, currentSelection, cursor, limit)` sorts
by handler key and returns a bounded page (default 20, maximum 100). Current
selection, including args, is independent of the returned page. The cursor marks
the last returned key and remains usable if that handler is removed.

A definition has at most one `InteractionHandler`. Its `Prepare(config)` exposes
safe description and effective argument schema; `ResolveArgs(config,args)`
validates a concrete destination. `ArgsForDestination(config, verifiedScope)`
derives arguments from verified input origin while checking every fixed field.
Runtime applies origin selection: an unambiguous match selects that handler; no
match or ambiguity selects dashboard only; originless inputs preserve selection.
