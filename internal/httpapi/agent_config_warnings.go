package httpapi

import (
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

var recommendedMachineTools = [...]string{
	toolcatalog.ToolNameRunCommand,
	toolcatalog.ToolNameWriteProcess,
	toolcatalog.ToolNameReadProcess,
	toolcatalog.ToolNameStopProcess,
	toolcatalog.ToolNameListProcesses,
	toolcatalog.ToolNameListMachines,
	toolcatalog.ToolNameInspectMachine,
	toolcatalog.ToolNameUploadArtifact,
	toolcatalog.ToolNameDownloadArtifact,
}

func agentConfigWarnings(contract agentconfig.RuntimeContract) []openapi.Warning {
	if len(contract.MachineSources) == 0 {
		return nil
	}
	enabled := make(map[string]struct{}, len(contract.Tools))
	for _, tool := range contract.Tools {
		enabled[tool.Name] = struct{}{}
	}
	missing := make([]string, 0, len(recommendedMachineTools))
	for _, name := range recommendedMachineTools {
		if _, ok := enabled[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return []openapi.Warning{{
		Code: openapi.MissingRecommendedMachineTools,
		Message: fmt.Sprintf(
			"Machine sources are configured, but some recommended machine tools are not enabled: %s. Add or enable them under tools so the agent can fully use its attached machines.",
			strings.Join(missing, ", "),
		),
	}}
}
