# App-owned config contract

App capabilities reference a saved project app by immutable name in source and
by public `app_…` ID in compiled configs. A project-scoped `ResolveAppName` returns
`AppResolution{AppID, AppType}`. The compiler resolves each distinct name once;
it persists pinned `AppID` values and tool policy, never definition metadata or
credentials. Public IDs use `publicid.KindProjectApp`.

```yaml
tools:
  app__engineering__post_message:
    permission: {mode: always_ask}
interaction_handlers:
  engineering: {}
```

Qualified tools need no `type` or operation field. Custom and MCP input schemas
remain independent; only `app__` and `mcp__` are reserved prefixes. Ordinary
custom names may contain `__`.

`AgentConfigSource.InteractionHandlers` is a
`map[string]AgentConfigAppCapabilitySource` with empty `{}` entries. The compiled
map contains `AppCapabilityCompiled{AppID string}`, keyed by app name. Tools use
`ToolCompiled.AppID` alongside enabled, permission and deferred settings.

Subscriptions are app-owned records attached at launch or through subscription
management. They are absent from source, compiled and runtime configs; source
`listeners` is rejected. Shipped subscription types are `thread_messages` for
Slack/Discord and `pull_request` for GitHub.
`appdefinition.Lookup(definition).Subscriptions[name].Prepare(conversation, events)`
validates one flat provider address and returns its concrete `Scope` and sorted
selected events. Omitted events mean all supported events; an explicit empty
selection is invalid. `Scope.Conversation()` returns the indexed routing kind/key.

Removing or reconfiguring a send tool leaves existing subscriptions intact.
Storage owns launch attachment transactions; saving or activating config and
posting messages do not create, update or delete subscriptions.

## Preparation and immutable authority

`RuntimeContractFromCompiled` validates and decodes without database I/O or new
default injection. It places ordinary tools in `.Tools`, pinned app tools in
`.AppTools`, and retains `.InteractionHandlers`. App tools are not
model-ready until preparation. `ReferencedAppIDs(compiled)` and
`contract.ReferencedAppIDs()` return the same sorted, distinct read set. Disabled
tools contribute no IDs; independently configured handlers still do.
Disabled entries remain stored and undergo the same strict name/AppID consistency
validation as enabled entries.

Callers load that read set once with project ownership and live status checks,
then call:

```go
PrepareAppTools(compiled Compiled, apps map[string]AppResolution) ([]RuntimeTool, error)
```

`apps` is keyed by pinned public app ID; each value contains the same `AppID` and
its immutable `AppType`. The result contains only prepared tools; unavailable
app capabilities are omitted. Structural errors still fail preparation. Merge
these tools with the ordinary runtime tools before model exposure. `RuntimeTool`
contains model-facing schema and policy, without app IDs.
Handlers are prepared independently during listing and selection. Credentials
are resolved separately, live, immediately before provider execution.

`toolcatalog.LookupAppTool(appType, operation)` returns an app operation.
`Prepare(qualifiedName)` creates its static action-only schema. Execution loads the
immutable conversation assigned at launch, validates the original arguments
against that schema, and passes the action to the provider implementation. Missing
context fails before provider I/O. Approval summaries use the assigned
`Scope.ConversationJSON()` without adding destination fields to the tool schema.

Pending calls must pass:

```go
ResolveAppToolAuthority(original, current RuntimeContract, name string, apps map[string]AppResolution) (AppToolAuthority, error)
ResolveInteractionHandlerAuthority(original, current RuntimeContract, key string, apps map[string]AppResolution) (PreparedAppInteractionHandler, error)
```

Tool authority requires unchanged pinned app ID and permission, with the tool
enabled and not denied in both configs. The result contains the original `.Tool`
and its `.Definition`. Handler authority requires the same app ID. Unrelated
edits do not invalidate a call. Reusing an app name cannot redirect a captured call or interaction.

## Composition and subagents

`AppCapabilitiesSource` has `Tools` and `InteractionHandlers` maps
with the same source entry types. The exported entry points are:

```go
CompileAppCapabilitiesSource(source AppCapabilitiesSource, opts CompileOptions) (Compiled, error)
DeriveWithAppCapabilities(base Compiled, additions AppCapabilitiesSource, opts CompileOptions) (Compiled, error)
```

The first compiles capabilities alone, without defaults or model/machine/skill
resolution. Derivation removes all existing keys before validating/resolving
additions. Existing disabled tools, permissions and handlers win completely; even
malformed or unavailable redundant additions are ignored.
Base model/machine/skill identities remain pinned. Launcher admission separately
registers concrete subscription attachments independently of config derivation.

`SubagentCompiledFrom` removes all app tools and handlers. Ordinary
custom tools, MCP and built-ins remain independent. Runtime subscriptions are
never inherited. This does not prohibit explicitly attaching subscriptions to an
existing subagent through the ordinary management contract.

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

A definition has at most one `InteractionHandler`. Its `Prepare()` exposes a
static complete-address schema; `ResolveArgs(args)` validates a concrete
destination independently of any sending context. Explicit handler selection
always supplies complete args. Verified input origins supply full arguments via
`Scope.ConversationJSON()`. Runtime applies origin selection: an unambiguous app
match selects that handler; no match or ambiguity selects dashboard only;
originless inputs preserve selection. Each prompt captures its full destination,
and dashboard responses remain available.
