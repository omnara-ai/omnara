package orglifecycle

import (
	_ "embed"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

//go:embed assistant_instruction.md
var assistantInstruction string

func compileAssistantPreset(
	input CreateAssistantAgentInput,
	model modelstore.ConfiguredModelSummaryRecord,
	secretID uuid.UUID,
) (agentconfig.Result, error) {
	publicSecret, err := publicid.Encode(publicid.KindSecret, secretID)
	if err != nil {
		return agentconfig.Result{}, err
	}
	allow := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow)
	ask := toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk)
	tools := make(map[string]agentconfig.AgentConfigMCPToolSource, len(input.ReadOnlyTools))
	for _, name := range input.ReadOnlyTools {
		tools[name] = agentconfig.AgentConfigMCPToolSource{Permission: &allow}
	}
	source, err := json.Marshal(agentconfig.AgentConfigSource{
		Instruction: assistantInstruction,
		Model:       agentconfig.AgentConfigModelSource{ProviderConfig: model.ProviderConfigName, Name: model.Name},
		Tools:       map[string]agentconfig.AgentConfigToolSource{"web_fetch": {Permission: &ask}},
		MCP: map[string]agentconfig.AgentConfigMCPSource{
			"omnara": {
				URL: input.MCPURL, Auth: &agentconfig.AgentConfigMCPAuthSource{Type: "bearer", SecretID: publicSecret},
				Deferred: true, Permission: &ask, Tools: tools,
			},
		},
	})
	if err != nil {
		return agentconfig.Result{}, err
	}
	supportsTools := true
	opts := input.AgentConfigOptions
	opts.ResolveModelSelection = func(provider, name string) (agentconfig.ResolvedModelSelection, error) {
		if provider != model.ProviderConfigName || name != model.Name {
			return agentconfig.ResolvedModelSelection{}, errors.New("unexpected assistant model reference")
		}
		return agentconfig.ResolvedModelSelection{ConfiguredModelID: model.ID, SupportsTools: &supportsTools}, nil
	}
	opts.ValidateSecretID = func(id uuid.UUID, kind secrets.Kind) error {
		if id != secretID || kind != secrets.KindGeneric {
			return errors.New("unexpected assistant secret reference")
		}
		return nil
	}
	return agentconfig.Compile(agentconfig.SourceFormatJSON, source, opts)
}
