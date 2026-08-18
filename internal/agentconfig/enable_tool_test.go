package agentconfig

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestEnableBuiltInToolPreservesStoredContract(t *testing.T) {
	source := `# preserve this comment
instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
machine_sources:
  - machine_pool_name: primary
tools:
  ask_question:
    enabled: false
  send_integration_message:
    enabled: false
    permission:
      mode: always_allow
`
	supportsTools := true
	poolID := testMachineSourcePublicID(t, publicid.KindMachinePool, "primary")
	compiled, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{
		ResolveModelSelection: func(string, string) (ResolvedModelSelection, error) {
			return ResolvedModelSelection{ConfiguredModelID: "model-original", SupportsTools: &supportsTools}, nil
		},
		ResolveMachinePoolName: func(string) (string, error) {
			return poolID, nil
		},
	})
	if err != nil {
		t.Fatalf("compile source: %v", err)
	}
	var original map[string]json.RawMessage
	if err := json.Unmarshal(compiled.CanonicalJSON, &original); err != nil {
		t.Fatalf("decode compiled source: %v", err)
	}
	original["future_contract_field"] = json.RawMessage(`{"keep":true}`)
	var tools map[string]json.RawMessage
	if err := json.Unmarshal(original["tools"], &tools); err != nil {
		t.Fatalf("decode compiled tools: %v", err)
	}
	var integrationTool map[string]json.RawMessage
	if err := json.Unmarshal(tools[toolcatalog.ToolNameSendIntegrationMessage], &integrationTool); err != nil {
		t.Fatalf("decode compiled integration tool: %v", err)
	}
	integrationTool["future_tool_field"] = json.RawMessage(`"keep"`)
	tools[toolcatalog.ToolNameSendIntegrationMessage], err = json.Marshal(integrationTool)
	if err != nil {
		t.Fatalf("encode compiled integration tool: %v", err)
	}
	original["tools"], err = json.Marshal(tools)
	if err != nil {
		t.Fatalf("encode compiled tools: %v", err)
	}
	originalJSON, err := json.Marshal(original)
	if err != nil {
		t.Fatalf("encode compiled source: %v", err)
	}

	updated, err := EnableBuiltInTool(
		SourceFormatYAML,
		[]byte(source),
		originalJSON,
		CompilerVersion,
		hashJSON(originalJSON),
		toolcatalog.ToolNameSendIntegrationMessage,
	)
	if err != nil {
		t.Fatalf("enable integration tool: %v", err)
	}
	if !strings.Contains(updated.Source, "# preserve this comment") {
		t.Fatalf("updated source lost comment:\n%s", updated.Source)
	}
	if len(updated.Compiled.MachineSources) != 1 ||
		updated.Compiled.MachineSources[0].MachinePoolID != poolID {
		t.Fatalf("updated machine sources = %+v", updated.Compiled.MachineSources)
	}
	parsed, err := ParseSource(SourceFormatYAML, []byte(updated.Source))
	if err != nil {
		t.Fatalf("parse updated source: %v", err)
	}
	send := parsed.Tools[toolcatalog.ToolNameSendIntegrationMessage]
	if send.Enabled == nil || !*send.Enabled || send.Permission == nil ||
		send.Permission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf("updated source integration tool = %+v", send)
	}
	if ask := parsed.Tools[toolcatalog.ToolNameAskQuestion]; ask.Enabled == nil || *ask.Enabled {
		t.Fatalf("updated source ask_question = %+v", ask)
	}
	var updatedRaw map[string]json.RawMessage
	if err := json.Unmarshal(updated.CanonicalJSON, &updatedRaw); err != nil {
		t.Fatalf("decode updated compiled source: %v", err)
	}
	if string(updatedRaw["future_contract_field"]) != `{"keep":true}` {
		t.Fatalf("future compiled field = %s", updatedRaw["future_contract_field"])
	}
	if err := json.Unmarshal(updatedRaw["tools"], &tools); err != nil {
		t.Fatalf("decode updated compiled tools: %v", err)
	}
	if err := json.Unmarshal(tools[toolcatalog.ToolNameSendIntegrationMessage], &integrationTool); err != nil {
		t.Fatalf("decode updated compiled integration tool: %v", err)
	}
	if string(integrationTool["future_tool_field"]) != `"keep"` ||
		string(integrationTool["enabled"]) != "true" {
		t.Fatalf("updated compiled integration tool = %s", tools[toolcatalog.ToolNameSendIntegrationMessage])
	}
	if _, err := RuntimeContractFromCompiled(
		updated.CanonicalJSON,
		updated.CompilerVersion,
		updated.Hash,
	); err != nil {
		t.Fatalf("validate updated runtime contract: %v", err)
	}
}

func TestEnableBuiltInToolSeparatesYAMLAliases(t *testing.T) {
	source := `instruction: Help the user make progress.
model:
  provider_config: openai-prod
  name: gpt-test
tools:
  send_integration_message: &shared
    enabled: false
  ask_question: *shared
`
	compiled, err := Compile(SourceFormatYAML, []byte(source), CompileOptions{})
	if err != nil {
		t.Fatalf("compile source: %v", err)
	}
	updated, err := EnableBuiltInTool(
		SourceFormatYAML,
		[]byte(source),
		compiled.CanonicalJSON,
		compiled.CompilerVersion,
		compiled.Hash,
		toolcatalog.ToolNameSendIntegrationMessage,
	)
	if err != nil {
		t.Fatalf("enable integration tool: %v", err)
	}
	parsed, err := ParseSource(SourceFormatYAML, []byte(updated.Source))
	if err != nil {
		t.Fatalf("parse updated source: %v", err)
	}
	if send := parsed.Tools[toolcatalog.ToolNameSendIntegrationMessage]; send.Enabled == nil || !*send.Enabled {
		t.Fatalf("updated integration tool = %+v", send)
	}
	if ask := parsed.Tools[toolcatalog.ToolNameAskQuestion]; ask.Enabled == nil || *ask.Enabled {
		t.Fatalf("updated ask_question = %+v", ask)
	}
}

func TestEnableBuiltInToolUpdatesJSONSource(t *testing.T) {
	source := []byte(
		`{"instruction":"Return <result> & explain.","model":{"provider_config":"openai-prod","name":"gpt-test"}}`,
	)
	compiled, err := Compile(SourceFormatJSON, source, CompileOptions{})
	if err != nil {
		t.Fatalf("compile source: %v", err)
	}
	updated, err := EnableBuiltInTool(
		SourceFormatJSON,
		source,
		compiled.CanonicalJSON,
		compiled.CompilerVersion,
		compiled.Hash,
		toolcatalog.ToolNameSendIntegrationMessage,
	)
	if err != nil {
		t.Fatalf("enable integration tool: %v", err)
	}
	parsed, err := ParseSource(SourceFormatJSON, []byte(updated.Source))
	if err != nil {
		t.Fatalf("parse updated source: %v", err)
	}
	if send, ok := parsed.Tools[toolcatalog.ToolNameSendIntegrationMessage]; !ok || send.Enabled != nil {
		t.Fatalf("updated integration tool = %+v, found=%v", send, ok)
	}
	if !strings.Contains(updated.Source, `Return <result> & explain.`) {
		t.Fatalf("updated source escaped instruction: %s", updated.Source)
	}
}
