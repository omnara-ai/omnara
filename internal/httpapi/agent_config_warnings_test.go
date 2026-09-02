package httpapi

import (
	"reflect"
	"testing"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestAgentConfigWarnings(t *testing.T) {
	completeTools := make([]agentconfig.RuntimeTool, 0, len(recommendedMachineTools))
	for _, name := range recommendedMachineTools {
		completeTools = append(completeTools, agentconfig.RuntimeTool{Name: name})
	}
	tests := []struct {
		name     string
		contract agentconfig.RuntimeContract
		want     []openapi.Warning
	}{
		{name: "no machine sources"},
		{
			name: "complete machine tools",
			contract: agentconfig.RuntimeContract{
				MachineSources: []agentconfig.RuntimeMachine{{}},
				Tools:          completeTools,
			},
		},
		{
			name: "missing machine tools",
			contract: agentconfig.RuntimeContract{
				MachineSources: []agentconfig.RuntimeMachine{{}},
				Tools: []agentconfig.RuntimeTool{
					{Name: toolcatalog.ToolNameRunCommand},
				},
			},
			want: []openapi.Warning{{
				Code: openapi.MissingRecommendedMachineTools,
				Message: "Machine sources are configured, but some recommended machine tools are not enabled: " +
					"write_process, read_process, stop_process, list_processes, list_machines, inspect_machine, upload_artifact, download_artifact. " +
					"Add or enable them under tools so the agent can fully use its attached machines.",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := agentConfigWarnings(test.contract); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("warnings = %v, want %v", got, test.want)
			}
		})
	}
}
