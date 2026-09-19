# Built-in app contributions

Apps are reviewed code contributions shipped with Omnara. `Lookup` in
[`definition.go`](definition.go) is the closed catalog: Slack, GitHub and Discord.
Project app instances configure these definitions; they cannot register a new
provider, scope type, event vocabulary or interaction transport.

See [Contributing an Omnara app](../integration/CONTRIBUTING.md) for the full
provider workflow and examples that reuse an existing provider.

Keep this package pure metadata and validation. A contribution belongs in the
existing owners:

- Define provider metadata and a typed `Scope` here. Specify validation,
  canonical conversation addresses and containment with provider examples in
  tests. Parent routing and launcher matching live in `events.go` and
  `launcher.go`.
- Declare ordinary provider tools in `internal/toolcatalog`; implement provider
  transport in `internal/integration` and tool execution in `internal/harness`.
  Storage owns authorization, locking, receipt admission and recovery.
- Extend the config scope schema in `internal/agentconfig/apps_schema.go` and
  its schema-to-Go correspondence test. Coordinate public API schema generation
  and durable provider constraints with their owners.

Tools, listeners, reply following and interaction presentation are independently
selected. Export only implemented capabilities: GitHub currently has no
interaction handler. Do not inject a universal send tool, grant one capability
because another is selected, or add a runtime registration DSL.

Hosted resources may bundle ordinary custom tools and MCP servers. Preserve their
global permissions and contribution provenance; subagents strip app authority
while retaining independent tools and servers. The detailed source, resolver and
compiled contracts are in [`../agentconfig/apps.md`](../agentconfig/apps.md).

Run the focused checks after a contribution:

```sh
OMNARA_REGEN_AGENT_CONFIG_SCHEMA=1 go test ./internal/agentconfig -run TestGeneratedAgentConfigSourceSchemaIsCurrent
go test -race ./internal/appdefinition ./internal/agentconfig
```
