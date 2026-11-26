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

// Both page tools are answered by the dashboard in the user's browser. Their timeouts are what
// the assistant waits when nobody has the dashboard open.
const (
	AssistantClickTool             = "dashboard_click"
	AssistantPointAtTool           = "dashboard_point_at"
	assistantClickTimeoutSeconds   = 5
	assistantPointAtTimeoutSeconds = 3

	assistantClickDescription = "Click something that is on the user's screen in the Omnara dashboard, the way " +
		"they would: they see your cursor go to it and click. It works on links, buttons, tabs, menus and the " +
		"items in an open menu, and it is the only way to take the user to another page. target is the control " +
		"as the newest page outline shows it, ref included, for example button \"New provider\" [e12]. A bare " +
		"name such as Models or a link's path also works when only one control answers to it. The click is real: " +
		"it does whatever that control does for the user, and cannot be undone. The result says what was clicked " +
		"and gives an outline of the screen afterwards, including a menu or dialog that opened. It fails when " +
		"nothing with that name is on screen, when several different controls share the name and no ref says " +
		"which, or when the ref's control has left the screen, and the failure shows what is there. It cannot " +
		"type. It times out after a few seconds when the user does not have the dashboard open."
	assistantPointAtDescription = "Point at a control, table row or heading on the page the user is looking at: " +
		"they see your cursor go to it, and it stays marked until they click. There is only one mark, so pointing " +
		"again moves it and the user sees only the last thing you pointed at. target is the thing as the newest " +
		"page outline shows it, ref included when it has one, for example button \"New provider\" [e12]; a bare " +
		"name such as New provider also works when only one thing answers to it. note is a few words shown next " +
		"to the mark, which is what the user reads while looking at the page, for example Add your provider " +
		"here. It changes nothing on the page. It fails when nothing on screen has that name or when several " +
		"different things share it and no ref says which, and the failure shows what is there. It times out " +
		"after a few seconds when the user does not have the dashboard open."

	assistantClickSchema = `{"type":"object","properties":{"target":{"type":"string"}},` +
		`"required":["target"],"additionalProperties":false}`
	assistantPointAtSchema = `{"type":"object","properties":{"target":{"type":"string"},"note":{"type":"string"}},` +
		`"required":["target"],"additionalProperties":false}`
)

func assistantPageTool(description, inputSchema string, timeoutSeconds int) agentconfig.AgentConfigToolSource {
	return agentconfig.AgentConfigToolSource{
		Type: "custom", Description: description, InputSchema: json.RawMessage(inputSchema),
		TimeoutSeconds: &timeoutSeconds,
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
			"web_fetch": {Permission: &allow},
			AssistantClickTool: assistantPageTool(
				assistantClickDescription, assistantClickSchema, assistantClickTimeoutSeconds),
			AssistantPointAtTool: assistantPageTool(
				assistantPointAtDescription, assistantPointAtSchema, assistantPointAtTimeoutSeconds),
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
