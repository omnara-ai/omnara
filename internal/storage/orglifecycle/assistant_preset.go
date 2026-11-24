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

//go:embed assistant_greeting.md
var assistantGreeting string

// The assistant's first input: an instruction to introduce itself, hidden from the conversation.
func assistantGreetingInput() (json.RawMessage, error) {
	return json.Marshal([]map[string]any{{
		"type": "text", "text": assistantGreeting, "metadata": map[string]string{"omnara_hidden": "true"},
	}})
}

// The assistant's page tools are answered by the dashboard in the user's browser. The timeout
// is what the assistant waits when nobody has the dashboard open.
const (
	AssistantOpenPageTool           = "dashboard_open_page"
	AssistantPointAtTool            = "dashboard_point_at"
	assistantPageToolTimeoutSeconds = 15

	assistantOpenPageSchema = `{"type":"object","properties":{"path":{"type":"string"}},` +
		`"required":["path"],"additionalProperties":false}`
	assistantPointAtSchema = `{"type":"object","properties":{"target":{"type":"string"},"note":{"type":"string"}},` +
		`"required":["target"],"additionalProperties":false}`

	assistantOpenPageDescription = "Open a page of the Omnara dashboard in the user's browser. Use it when the user " +
		"asks to be taken somewhere, or before pointing at something that is on another page. path is a dashboard " +
		"path such as /models or /projects/{project_id}/agents, taken from the list of dashboard pages or from a " +
		"link in a page outline. The result is an outline of the page once it has opened. It fails when the path " +
		"is not a dashboard page, and times out when the user does not have the dashboard open."
	assistantPointAtDescription = "Mark a control, table row or heading on the page the user is looking at so they " +
		"can see where it is. target is its name as a page outline shows it, for example New provider. note is an " +
		"optional few words shown next to it, for example Add your provider here. The mark stays until the user " +
		"clicks. It fails when nothing on the page has that name, and returns the page's outline so you can pick a " +
		"name that is there. It times out when the user does not have the dashboard open."
)

func assistantPageTool(description, inputSchema string) agentconfig.AgentConfigToolSource {
	timeout := assistantPageToolTimeoutSeconds
	return agentconfig.AgentConfigToolSource{
		Type: "custom", Description: description, InputSchema: json.RawMessage(inputSchema), TimeoutSeconds: &timeout,
	}
}

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
	source, err := json.Marshal(agentconfig.AgentConfigSource{
		Instruction: assistantInstruction,
		Model:       agentconfig.AgentConfigModelSource{ProviderConfig: model.ProviderConfigName, Name: model.Name},
		Tools: map[string]agentconfig.AgentConfigToolSource{
			"web_fetch":           {Permission: &allow},
			AssistantOpenPageTool: assistantPageTool(assistantOpenPageDescription, assistantOpenPageSchema),
			AssistantPointAtTool:  assistantPageTool(assistantPointAtDescription, assistantPointAtSchema),
		},
		MCP: map[string]agentconfig.AgentConfigMCPSource{
			"omnara": {
				URL: input.MCPURL, Auth: &agentconfig.AgentConfigMCPAuthSource{Type: "bearer", SecretID: publicSecret},
				Deferred: true, Permission: &allow,
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
