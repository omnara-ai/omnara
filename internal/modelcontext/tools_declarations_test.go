package modelcontext

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestToolDeclarationsPreserveSchemasAndEffectivePermissionsAcrossSources(t *testing.T) {
	t.Parallel()
	catalog, err := toolcatalog.Default()
	require.NoError(t, err)
	run, ok := catalog.Lookup(toolcatalog.ToolNameRunCommand)
	require.True(t, ok)
	question, ok := catalog.Lookup(toolcatalog.ToolNameAskQuestion)
	require.True(t, ok)
	schema := json.RawMessage(`{"type":"object","properties":{"omnara_channel":{"type":"integer"}},"required":["omnara_channel"],"additionalProperties":false}`)
	ask := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
	allow := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)
	deny := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
	contract := agentconfig.RuntimeContract{
		Tools: []agentconfig.RuntimeTool{
			{Name: run.Name, Type: toolcatalog.ToolTypeBuiltIn, InputSchema: run.InputSchema, Permission: ask},
			{
				Name: question.Name, Type: toolcatalog.ToolTypeBuiltIn,
				InputSchema: question.InputSchema, Permission: question.DefaultPermission,
			},
			{
				Name: "custom_ask", Type: toolcatalog.ToolTypeCustom, Description: "Custom ask.",
				InputSchema: schema, Permission: ask,
			},
			{
				Name: "custom_allow", Type: toolcatalog.ToolTypeCustom, Description: "Custom allow.",
				InputSchema: schema, Permission: allow,
			},
			{
				Name: "custom_deny", Type: toolcatalog.ToolTypeCustom, Description: "Custom deny.",
				InputSchema: schema, Permission: deny,
			},
		},
		MCPServers: []agentconfig.RuntimeMCPServer{{
			ServerKey: "docs", DefaultEnabled: true, Permission: ask,
			Tools: map[string]agentconfig.RuntimeMCPTool{"allow": {Permission: &allow}, "deny": {Permission: &deny}},
		}},
	}
	snapshot, err := json.Marshal([]mcpToolSnapshot{
		{Name: "ask", Description: "Remote ask.", InputSchema: schema},
		{Name: "allow", Description: "Remote allow.", InputSchema: schema},
		{Name: "deny", Description: "Remote deny.", InputSchema: schema},
	})
	require.NoError(t, err)
	store := &fakeContextStore{mcpConnections: map[string]executionstore.MCPConnectionRecord{
		"docs": {ServerKey: "docs", State: executionstore.MCPConnectionStateReady, ToolsSnapshot: snapshot},
	}}
	before, err := json.Marshal(contract)
	require.NoError(t, err)
	snapshotBefore := bytes.Clone(snapshot)
	first, err := RuntimeContractToolSpecs(context.Background(), store, testProjectID, testAgentID, contract, time.Time{})
	require.NoError(t, err)
	second, err := RuntimeContractToolSpecs(context.Background(), store, testProjectID, testAgentID, contract, time.Time{})
	require.NoError(t, err)
	require.Equal(t, first, second)
	want := map[string]struct {
		description string
		schema      json.RawMessage
		permission  toolpermission.Selection
	}{
		run.Name:           {run.Description + " Requires approval before execution.", run.InputSchema, ask},
		question.Name:      {question.Description, question.InputSchema, question.DefaultPermission},
		"custom_ask":       {"Custom ask. Requires approval before execution.", schema, ask},
		"custom_allow":     {"Custom allow.", schema, allow},
		"custom_deny":      {"Custom deny.", schema, deny},
		"mcp__docs__ask":   {`MCP tool "ask" from server "docs". Remote ask. Requires approval before execution.`, schema, ask},
		"mcp__docs__allow": {`MCP tool "allow" from server "docs". Remote allow.`, schema, allow},
		"mcp__docs__deny":  {`MCP tool "deny" from server "docs". Remote deny.`, schema, deny},
	}
	require.Len(t, first, len(want))
	for _, spec := range first {
		expected, found := want[spec.Name]
		require.True(t, found)
		require.Equal(t, expected.description, spec.Description)
		require.Equal(t, expected.schema, spec.InputSchema)
		require.Equal(t, expected.permission, spec.Permission)
	}
	after, err := json.Marshal(contract)
	require.NoError(t, err)
	require.Equal(t, before, after)
	require.Equal(t, snapshotBefore, snapshot)
}

func TestToolDeclarationsPreserveEveryBuiltinWithoutArgumentInjection(t *testing.T) {
	t.Parallel()
	catalog, err := toolcatalog.Default()
	require.NoError(t, err)
	for _, entry := range catalog.Entries() {
		t.Run(entry.Name, func(t *testing.T) {
			t.Parallel()
			permission := entry.DefaultPermission
			if _, mayAsk := toolpermission.FindMode(entry.PermissionModes, toolpermission.ModeAlwaysAsk); mayAsk {
				permission = toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
			}
			contract := agentconfig.RuntimeContract{Tools: []agentconfig.RuntimeTool{{
				Name: entry.Name, Type: toolcatalog.ToolTypeBuiltIn, InputSchema: entry.InputSchema, Permission: permission,
			}}}
			specs, err := RuntimeContractToolSpecs(context.Background(), nil, testProjectID, testAgentID, contract, time.Time{})
			require.NoError(t, err)
			require.Len(t, specs, 1)
			require.Equal(t, entry.InputSchema, specs[0].InputSchema)
			description := entry.Description
			if permission.Mode == toolpermission.ModeAlwaysAsk {
				description += " Requires approval before execution."
			}
			require.Equal(t, description, specs[0].Description)
		})
	}
}

func TestToolDeclarationsKeepExecutionMetadataPrivate(t *testing.T) {
	t.Parallel()
	spec := ToolSpec{Name: "custom", Description: "Original.", InputSchema: json.RawMessage(`{"type":"object"}`),
		Type: toolcatalog.ToolTypeCustom, Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)}
	raw, err := json.Marshal(spec)
	require.NoError(t, err)
	var shape map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &shape))
	require.Len(t, shape, 3)
	require.Contains(t, shape, "name")
	require.Contains(t, shape, "description")
	require.Contains(t, shape, "input_schema")
}

func TestToolDeclarationsPreserveSchemasWithoutProjectionRestrictions(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		schema, validInput string
	}{
		"business name":  {`{"type":"object","properties":{"omnara_channel":{"type":"integer"}},"required":["omnara_channel"]}`, `{"omnara_channel":9007199254740993}`},
		"propertyNames":  {`{"type":"object","propertyNames":{"pattern":"^[a-z]+$"}}`, `{"name":true}`},
		"enum":           {`{"type":"object","enum":[{"action":"start"}]}`, `{"action":"start"}`},
		"const":          {`{"type":"object","const":{}}`, `{}`},
		"root recursion": {`{"type":"object","properties":{"child":{"$ref":"#"}},"additionalProperties":false}`, `{"child":{}}`},
	} {
		for _, mode := range []string{toolpermission.ModeAlwaysAsk, toolpermission.ModeAlwaysAllow} {
			for _, source := range []string{"custom", "mcp"} {
				t.Run(name+"/"+mode+"/"+source, func(t *testing.T) {
					t.Parallel()
					permission := toolpermission.DefaultSelection(mode)
					contract := agentconfig.RuntimeContract{}
					var store *fakeContextStore
					toolName := "custom"
					if source == "mcp" {
						contract.MCPServers = []agentconfig.RuntimeMCPServer{{
							ServerKey: "docs", DefaultEnabled: true, Permission: permission,
						}}
						snapshot := `[{"name":"remote"`
						if test.schema != "" {
							snapshot += `,"inputSchema":` + test.schema
						}
						snapshot += `}]`
						store = &fakeContextStore{mcpConnections: map[string]executionstore.MCPConnectionRecord{
							"docs": {ServerKey: "docs", State: executionstore.MCPConnectionStateReady, ToolsSnapshot: json.RawMessage(snapshot)},
						}}
					} else {
						contract.Tools = []agentconfig.RuntimeTool{{
							Name: toolName, Type: toolcatalog.ToolTypeCustom,
							InputSchema: json.RawMessage(test.schema), Permission: permission,
						}}
					}
					specs, err := RuntimeContractToolSpecs(
						context.Background(), store, testProjectID, testAgentID, contract, time.Time{},
					)
					require.NoError(t, err)
					require.Len(t, specs, 1)
					require.Equal(t, json.RawMessage(test.schema), specs[0].InputSchema)
					require.NoError(t, jsonschema.Validate(specs[0].InputSchema, json.RawMessage(test.validInput)))
				})
			}
		}
	}
}

func TestRuntimeToolReconstructionPreservesBuiltinWithUnrelatedMCPSchema(t *testing.T) {
	t.Parallel()
	for _, schema := range []string{"", "null", `{"type":"invalid"}`} {
		t.Run(schema, func(t *testing.T) {
			t.Parallel()
			contract := agentconfig.RuntimeContract{MCPServers: []agentconfig.RuntimeMCPServer{{
				ServerKey: "docs", DefaultEnabled: true,
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
			}}}
			contract, err := contract.WithImplicitBuiltInTool(toolcatalog.ToolNameListProcesses)
			require.NoError(t, err)
			entry := map[string]any{"name": "lookup"}
			if schema != "" {
				entry["inputSchema"] = json.RawMessage(schema)
			}
			catalog, err := json.Marshal([]any{entry})
			require.NoError(t, err)
			store := &fakeContextStore{mcpConnections: map[string]executionstore.MCPConnectionRecord{
				"docs": {ServerKey: "docs", State: executionstore.MCPConnectionStateReady, ToolsSnapshot: catalog},
			}}
			specs, err := RuntimeContractToolSpecs(
				context.Background(), store, testProjectID, testAgentID, contract, time.Time{},
			)
			require.NoError(t, err)
			require.Len(t, specs, 2)
			require.True(t, HasTool(specs, toolcatalog.ToolNameListProcesses))
			require.False(t, HasTool(specs, toolcatalog.ToolNameReadFile))
			require.False(t, HasTool(specs, toolcatalog.ToolNameSearchFiles))
			for _, spec := range specs {
				if spec.Name == "mcp__docs__lookup" {
					want := schema
					if want == "" || want == "null" {
						want = `{"type":"object","properties":{}}`
					}
					require.Equal(t, json.RawMessage(want), spec.InputSchema)
				}
			}
		})
	}
}

func TestApprovalDescriptionNamesOnlyAvailableChannelSetter(t *testing.T) {
	t.Parallel()
	contract := agentconfig.RuntimeContract{Tools: []agentconfig.RuntimeTool{
		{Name: "lookup", Description: "Look up an item.", Type: toolcatalog.ToolTypeCustom,
			InputSchema: json.RawMessage(`{"type":"object"}`), Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)},
	}}
	var err error
	contract, err = contract.WithImplicitBuiltInTool(toolcatalog.ToolNameSetCurrentChannel)
	require.NoError(t, err)
	specs, err := RuntimeContractToolSpecs(context.Background(), nil, testProjectID, testAgentID, contract, time.Time{})
	require.NoError(t, err)
	for _, spec := range specs {
		if spec.Name == "lookup" {
			require.Equal(t,
				"Look up an item. Requires approval before execution. Use set_current_channel to choose where approval prompts are sent.",
				spec.Description,
			)
			return
		}
	}
	t.Fatal("custom declaration missing")
}

func TestConditionalApprovalDescriptionPreservesToolContract(t *testing.T) {
	t.Parallel()
	for _, canSelectChannel := range []bool{false, true} {
		contract := agentconfig.RuntimeContract{Tools: []agentconfig.RuntimeTool{{
			Name: "conditional", Type: toolcatalog.ToolTypeCustom, Description: "Original description.",
			InputSchema: json.RawMessage(`{"type":"object"}`),
			Permission:  toolpermission.Selection{Mode: "conditional", Parameters: json.RawMessage(`{"rule":"ask"}`)},
		}}}
		if canSelectChannel {
			var err error
			contract, err = contract.WithImplicitBuiltInTool(toolcatalog.ToolNameSetCurrentChannel)
			require.NoError(t, err)
		}
		specs, err := RuntimeContractToolSpecs(t.Context(), nil, testProjectID, testAgentID, contract, time.Time{})
		require.NoError(t, err)
		want := "Original description. May require approval before execution."
		if canSelectChannel {
			want += " Use set_current_channel to choose where approval prompts are sent."
		}
		require.Equal(t, want, specs[0].Description)
		require.Equal(t, contract.Tools[0].InputSchema, specs[0].InputSchema)
		require.Equal(t, contract.Tools[0].Permission, specs[0].Permission)
	}
}
