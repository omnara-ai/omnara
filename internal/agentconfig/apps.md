# App resource compiler contract

`AgentConfigSource.AppResources` attaches independently selected tools, listeners,
reply-follow policies and interaction handlers. The resource map key is local to
the config. `definition` identifies a built-in app shipped through a code
contribution (`omnara.slack`, `omnara.github`, or `omnara.discord`); `app_instance`
instead references reusable project setup. Launchers are project settings and
are not accepted in an agent config.

```yaml
app_resources:
  support:
    definition: omnara.slack
    connection: iin_aeaqcaibaeaqcaibaeaqcaibae
    scope:
      slack:
        channel_id: C123
        thread_ts: "111.222333"
    tools:
      slack_read: {}
      slack_post_message: {}
    listener:
      events: [message]
    interaction_handler:
      definition: omnara.slack.interactions
tools:
  slack_post_message:
    permission: {mode: always_ask}
```

The example connection ID is illustrative. Connection resolution checks the
public ID against the current project, provider and connection availability.

`ResolveAppInstance` returns `AppInstanceResolution{AppInstanceID, Resource}`.
The returned resource is inline and supplies a default connection/scope and
available ordinary tool/MCP definitions. Source `tools` and `mcp` maps select
exported names. An empty entry uses the bundled definition. Enablement and
permissions may be overridden, but a different schema, description, endpoint or
auth identity is rejected. A source scope replaces the whole default scope;
there is no recursive scope merge or placeholder interpolation.

Omitting source tools, MCP, listener, follow or handler selects none of that
capability, even when instance settings contain it. A handler needs no ordinary
listener or send tool. A follow policy needs no whole-channel listener. Selected
tools, listeners, follow policies and handlers require a connection and concrete
scope. Hosted app bundles may also contribute ordinary custom tools and MCP
servers; an MCP-only resource needs neither a connection nor a scope. Credentials
use existing MCP secret references and project connections; resource metadata
has no credential fields. Ordinary top-level custom tools and MCP declarations
remain independent of apps.

`ValidateAppResourceTemplate` validates reusable inline setup without an agent
model or reference resolver. Scope may be absent for a launcher to supply later.
Any supplied scope and every exported definition must already be valid. Project
authorization of references and launcher validation remain storage/service work.

Compilation expands selected bundles into the existing top-level `Tools` and
`MCP` maps. Repeated definitions and effective policies must agree after applying
explicit global tool enablement, permission and deferred overrides; no union of
conflicting policies or schemas is inferred. A policy-only
global entry can name an app custom tool without recopying its definition. Base
MCP declarations override policy on the same endpoint/auth identity before bundle
conflicts are compared; their own remote-tool policy remains authoritative.
Pinned derivation applies all existing tool and MCP policies, including built-in
tools, at that same stage. Only entries present before expansion are authoritative:
without a base policy, different new bundle defaults still conflict. Applying
pinned policy never changes independent versus app-only provenance.

When an inactive resource or selected custom tool contributes no tool entry,
compiled-only `app_tool_policies` retains its explicit global enabled, permission
and deferred override. This map carries no definition or runtime authority and
does not count as configured tools. Pinned derivation applies it through ordinary
tool compilation before comparing bundles. The override remains available to all
new bundles in a derivation, then is consumed once a `ToolCompiled` carries the
effective policy, including an explicitly disabled entry. Unused policies survive
canonical round trips and successive derivations. Runtime validation rejects
invalid/reserved names, invalid policies and simultaneous map/tool carriers.
Both self and profile subagent derivation discard the map along with app authority.
This annotation is not accepted in config source.

Compiled resources retain concrete connection IDs/scopes, selected names,
listener/follow/handler declarations and optional instance provenance. Runtime
does not resolve instances again. Provider action schemas expose a `resource`
enum of enabled attachments that select that action; the selector is required
when ambiguous. Custom tool schemas are unchanged.

`AppOrigin.ResourceKeys` on contributed tools/MCP servers must agree with the
compiled resource declarations in both directions. `Base` records an independent
base declaration, not a policy-only override on an app custom tool or provider
action. Both self and profile subagent derivation remove resources and app-only
contributions, retaining independent base tools/servers without app provenance.
No name-prefix stripping or MCP remote-tool union is performed.

For migration, an enabled or disabled provider tool entry without resources is
valid policy. It is not exposed at runtime until a resource supplies authority.
`slack_post_message` supports `artifact_ids`. The retired generic integration
tools are no longer registered; the coordinated migration rewrites stored
policies, including historical configs, before the current runtime loads them.
Preflight existing custom tool names against new provider
built-in names: collisions are errors, never silent conversions.

Pure scope helpers live in `appdefinition`: `Conversation()` returns a canonical
kind/key within a connection; `Contains` checks provider scope only. Callers must
also check connection identity and live authorization. Slack preserves its
existing `channel:thread_ts` and DM addresses. GitHub scope is a PR; review thread
targeting remains provider-tool arguments inside that PR.
