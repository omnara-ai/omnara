package httpapi

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestAgentConfigWarnings(t *testing.T) {
	completeTools := make([]agentconfig.RuntimeTool, 0, len(recommendedMachineTools))
	for _, name := range recommendedMachineTools {
		completeTools = append(completeTools, agentconfig.RuntimeTool{Name: name})
	}
	legacyTools := append([]agentconfig.RuntimeTool(nil), completeTools...)
	for i := range legacyTools {
		switch legacyTools[i].Name {
		case toolcatalog.ToolNameUploadFile:
			legacyTools[i].Name = toolcatalog.ToolNameUploadArtifact
		case toolcatalog.ToolNameDownloadFile:
			legacyTools[i].Name = toolcatalog.ToolNameDownloadArtifact
		}
	}
	tests := []struct {
		name     string
		contract agentconfig.RuntimeContract
		want     []openapi.Warning
	}{
		{name: "no machine sources"},
		{
			name: "legacy machine tools",
			contract: agentconfig.RuntimeContract{
				MachineSources: []agentconfig.RuntimeMachine{{}},
				Tools:          legacyTools,
			},
		},
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
					"write_process, read_process, stop_process, list_processes, list_machines, inspect_machine, upload_file, download_file. " +
					"Add or enable them under tools so the agent can fully use its attached machines.",
			}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got := agentConfigWarnings(test.contract)
			if diff := cmp.Diff(test.want, got); diff != "" {
				t.Fatalf("warnings mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
