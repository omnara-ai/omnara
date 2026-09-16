package agentconfig

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/stretchr/testify/require"
)

func compileRuntimeContract(t *testing.T, source string) RuntimeContract {
	t.Helper()
	compiled, err := Compile(SourceFormatYAML, []byte(validAgentSource(source)), CompileOptions{})
	require.NoError(t, err)
	contract, err := RuntimeContractFromCompiled(json.RawMessage(compiled.CanonicalJSON), CompilerVersion, compiled.Hash)
	require.NoError(t, err)
	return contract
}

func runtimeToolByName(contract RuntimeContract, name string) (RuntimeTool, bool) {
	for _, tool := range contract.Tools {
		if tool.Name == name {
			return tool, true
		}
	}
	return RuntimeTool{}, false
}

func TestDeferredToolsAddImplicitToolSearch(t *testing.T) {
	contract := compileRuntimeContract(t, `
tools:
  web_search: {}
  web_fetch:
    deferred: true
  create_ticket:
    type: custom
    deferred: true
    description: Create a ticket.
    input_schema:
      type: object
      properties:
        title:
          type: string
`)
	webFetch, ok := runtimeToolByName(contract, toolcatalog.ToolNameWebFetch)
	require.True(t, ok)
	require.True(t, webFetch.Deferred)
	webSearch, ok := runtimeToolByName(contract, toolcatalog.ToolNameWebSearch)
	require.True(t, ok)
	require.False(t, webSearch.Deferred)
	ticket, ok := runtimeToolByName(contract, "create_ticket")
	require.True(t, ok)
	require.True(t, ticket.Deferred)
	search, ok := runtimeToolByName(contract, toolcatalog.ToolNameToolSearch)
	require.True(t, ok)
	require.False(t, search.Deferred)
	require.True(t, contract.DefersAnyTool())
}

func TestNoDeferredToolsSkipsToolSearch(t *testing.T) {
	contract := compileRuntimeContract(t, `
tools:
  web_search: {}
`)
	_, ok := runtimeToolByName(contract, toolcatalog.ToolNameToolSearch)
	require.False(t, ok)
	require.False(t, contract.DefersAnyTool())
}

func TestMCPDeferredResolution(t *testing.T) {
	contract := compileRuntimeContract(t, `
mcp:
  crm:
    url: https://example.com/mcp
    deferred: true
    tools:
      search:
        deferred: false
  docs:
    url: https://example.com/docs
    tools:
      lookup:
        deferred: true
`)
	require.Len(t, contract.MCPServers, 2)
	crm := contract.MCPServers[0]
	require.Equal(t, "crm", crm.ServerKey)
	resolution, ok := crm.ResolveTool("search")
	require.True(t, ok)
	require.False(t, resolution.Deferred)
	resolution, ok = crm.ResolveTool("anything_else")
	require.True(t, ok)
	require.True(t, resolution.Deferred)
	docs := contract.MCPServers[1]
	resolution, ok = docs.ResolveTool("lookup")
	require.True(t, ok)
	require.True(t, resolution.Deferred)
	resolution, ok = docs.ResolveTool("other")
	require.True(t, ok)
	require.False(t, resolution.Deferred)
	_, ok = runtimeToolByName(contract, toolcatalog.ToolNameToolSearch)
	require.True(t, ok)
}

func TestToolSearchCannotBeDeferred(t *testing.T) {
	_, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  tool_search:
    deferred: true
`)), CompileOptions{})
	require.ErrorContains(t, err, "tool_search cannot be deferred")
}

func TestCustomToolCannotUseReservedWireName(t *testing.T) {
	_, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
tools:
  call_deferred_tool:
    type: custom
    description: Nope.
    input_schema:
      type: object
`)), CompileOptions{})
	require.ErrorContains(t, err, "reserved")
}
