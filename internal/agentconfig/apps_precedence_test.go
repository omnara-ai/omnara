package agentconfig

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestAppBundleConflictsUseEffectiveGlobalPolicy(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{toolpermission.ModeAlwaysAsk, toolpermission.ModeAlwaysDeny} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			first, second := customAppTestTool(), customAppTestTool()
			allow := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)
			ask := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
			first.Permission, second.Permission = &allow, &ask
			source := appTestSource(map[string]AgentConfigAppResourceSource{
				"first":  appPolicyTestResource(t, first),
				"second": appPolicyTestResource(t, second),
			})
			policy := toolpermission.DefaultSelection(mode)
			source.Tools = map[string]AgentConfigToolSource{"ticket": {Permission: &policy}}
			result, err := compileAppTest(t, source, appTestOptions())
			require.NoError(t, err)
			require.Equal(t, mode, result.Compiled.Tools["ticket"].Permission.Mode)
			require.Equal(t, []string{"first", "second"}, result.Compiled.Tools["ticket"].AppOrigin.ResourceKeys)
			require.False(t, result.Compiled.Tools["ticket"].AppOrigin.Base)
			contract, err := RuntimeContractFromCompiled(result.CanonicalJSON, CompilerVersion, result.Hash)
			require.NoError(t, err)
			ticketCount := 0
			for _, runtimeTool := range contract.Tools {
				if runtimeTool.Name == "ticket" {
					ticketCount++
					require.Equal(t, mode, runtimeTool.Permission.Mode)
				}
			}
			require.Equal(t, 1, ticketCount)
			child, err := SubagentCompiledFrom(result.Compiled,
				SubagentCompiled{Type: SubagentTypeSelf}, SubagentDepth{Depth: 1}, nil)
			require.NoError(t, err)
			require.NotContains(t, child.Tools, "ticket", "global policy is not independent tool authority")

			// Without a global choice these defaults still conflict; resource order
			// must not silently choose a permission for the shared tool name.
			source.Tools = nil
			_, err = compileAppTest(t, source, appTestOptions())
			require.ErrorContains(t, err, "conflicting")
		})
	}
}

func TestGlobalAppToolPolicyCannotResolveDefinitionConflicts(t *testing.T) {
	t.Parallel()
	for _, field := range []string{"description", "schema"} {
		t.Run(field, func(t *testing.T) {
			t.Parallel()
			first, second := customAppTestTool(), customAppTestTool()
			if field == "description" {
				second.Description = "Different tool definition."
			} else {
				second.InputSchema["required"] = []any{}
			}
			source := appTestSource(map[string]AgentConfigAppResourceSource{
				"first":  appPolicyTestResource(t, first),
				"second": appPolicyTestResource(t, second),
			})
			deny := toolpermission.DefaultSelection(toolpermission.ModeAlwaysDeny)
			source.Tools = map[string]AgentConfigToolSource{"ticket": {Permission: &deny}}
			_, err := compileAppTest(t, source, appTestOptions())
			require.ErrorContains(t, err, "conflict")
		})
	}
}
