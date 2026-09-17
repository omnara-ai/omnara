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

func TestToolSearchDefaultMatchesCompiledAndPreview(t *testing.T) {
	for _, test := range []struct {
		name       string
		source     string
		wantSearch bool
		wantOn     bool
	}{
		{"no deferred tools", "tools: {web_search: {}}\n", false, false},
		{"deferred built-in", "tools: {web_fetch: {deferred: true}}\n", true, true},
		{"deferred but disabled", "tools: {web_search: {}, web_fetch: {deferred: true, enabled: false}}\n", false, false},
		{"deferred MCP server", "mcp: {docs: {url: https://example.com/mcp, deferred: true}}\n", true, true},
		{"deferred MCP tool", "mcp: {docs: {url: https://example.com/mcp, tools: {lookup: {deferred: true}}}}\n", true, true},
		{"explicitly disabled", "tools: {web_fetch: {deferred: true}, tool_search: {enabled: false}}\n", true, false},
		{"explicitly configured", "tools: {web_fetch: {deferred: true}, tool_search: {}}\n", true, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := validAgentSource(test.source)
			result, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{})
			require.NoError(t, err)
			require.Equal(t, source, result.Source)
			compiledSearch, compiledHas := result.Compiled.Tools[toolcatalog.ToolNameToolSearch]
			require.Equal(t, test.wantSearch, compiledHas)
			if compiledHas {
				require.Equal(t, test.wantOn, compiledSearch.Enabled)
			}

			preview, err := ToolsFromSource(SourceFormatYAML, []byte(source))
			require.NoError(t, err)
			previewHas := false
			for _, tool := range preview {
				if tool.Name != toolcatalog.ToolNameToolSearch {
					continue
				}
				previewHas = true
				require.Equal(t, test.wantOn, tool.Enabled)
				require.Equal(t, compiledSearch.Permission, tool.Permission)
			}
			require.Equal(t, test.wantSearch, previewHas)

			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, result.CompilerVersion, result.Hash)
			require.NoError(t, err)
			runtimeSearch, runtimeHas := runtimeToolByName(contract, toolcatalog.ToolNameToolSearch)
			require.Equal(t, test.wantOn, runtimeHas)
			if runtimeHas {
				require.Equal(t, compiledSearch.Permission, runtimeSearch.Permission)
			}
		})
	}
}

func TestRuntimeDoesNotAddToolSearch(t *testing.T) {
	source := validAgentSource("tools: {web_fetch: {deferred: true}}\n")
	result, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{})
	require.NoError(t, err)
	delete(result.Compiled.Tools, toolcatalog.ToolNameToolSearch)
	encoded, err := EncodeCompiled(result.Compiled)
	require.NoError(t, err)
	contract, err := RuntimeContractFromCompiled(encoded.CanonicalJSON, CompilerVersion, encoded.Hash)
	require.NoError(t, err)
	_, ok := runtimeToolByName(contract, toolcatalog.ToolNameToolSearch)
	require.False(t, ok)
}
