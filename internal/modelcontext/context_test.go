package modelcontext

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/skillstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func contextFixtureJSON(t testing.TB, value any) json.RawMessage {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal model-context fixture JSON: %v", err)
	}
	return body
}

func contextTextEvent(t testing.TB, id storage.ID, sequence int64, text string) executionstore.ContextEventRecord {
	t.Helper()
	return executionstore.ContextEventRecord{
		ID:           id,
		ProjectID:    testProjectID,
		AgentID:      testAgentID,
		Sequence:     sequence,
		Role:         modelprotocol.RoleUser,
		ContentParts: contextFixtureJSON(t, []map[string]string{{"type": "text", "text": text}}),
	}
}

func TestBuildKeepsAllMessagesAndCrossTurnToolResultsThroughWatermark(t *testing.T) {
	store := &fakeContextStore{watermark: 760}
	for i := 1; i <= 150; i++ {
		store.messages = append(store.messages, contextTextEvent(t, testIDN(i), int64(i), fmt.Sprintf("message %03d", i)))
	}
	turnID := testIDN(300)
	store.toolCalls = []executionstore.ToolCallRecord{
		{
			ID:                      testIDN(750),
			ProviderCallID:          "tool_750",
			TurnID:                  testIDN(301),
			Name:                    "run_command",
			Input:                   contextFixtureJSON(t, map[string]string{"command": "echo 750"}),
			ModelCallContextID:      testIDN(749),
			ToolCallResultID:        testIDN(1750),
			ToolResultEventID:       testIDN(2750),
			SourceEventSequence:     149,
			ToolResultEventSequence: 750,
			State:                   executionstore.ToolCallStateCompleted,
			Outcome:                 executionstore.ToolResultOutcomeSucceeded,
			ResultContentParts: contextFixtureJSON(t, []map[string]any{{
				"type":  "structured_data",
				"value": map[string]any{"i": 750},
			}}),
		},
		{
			ID:                      testIDN(760),
			ProviderCallID:          "tool_760",
			TurnID:                  turnID,
			Name:                    "run_command",
			Input:                   json.RawMessage(`{}`),
			ModelCallContextID:      testIDN(759),
			ToolCallResultID:        testIDN(1760),
			ToolResultEventID:       testIDN(2760),
			SourceEventSequence:     150,
			ToolResultEventSequence: 760,
			State:                   executionstore.ToolCallStateCompleted,
			Outcome:                 executionstore.ToolResultOutcomeSucceeded,
			ResultContentParts: contextFixtureJSON(t, []map[string]any{{
				"type":  "structured_data",
				"value": map[string]any{"i": 760},
			}}),
		},
	}

	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          turnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(bundle.Messages) != 150 {
		t.Fatalf("expected all messages after checkpoint, got %d", len(bundle.Messages))
	}
	if bundle.Messages[0].ID != testIDN(1).String() ||
		bundle.Messages[len(bundle.Messages)-1].ID != testIDN(150).String() {
		t.Fatalf(
			"expected message range msg_001..msg_150, got %s..%s",
			bundle.Messages[0].ID,
			bundle.Messages[len(bundle.Messages)-1].ID,
		)
	}
	if len(bundle.ToolResults) != 2 ||
		bundle.ToolResults[0].ToolCallID != testIDN(750).String() ||
		bundle.ToolResults[1].ToolCallID != testIDN(760).String() {
		t.Fatalf("expected retained prior- and current-turn tool results, got %+v", bundle.ToolResults)
	}
	if bundle.ToolResults[0].Outcome != executionstore.ToolResultOutcomeSucceeded ||
		!strings.HasPrefix(
			string(bundle.ToolResults[0].ContentParts),
			`[{"type":"structured_data","value":{"outcome":"succeeded"}}`,
		) {
		t.Fatalf("tool result omitted its outcome: %+v", bundle.ToolResults[0])
	}
	if bundle.ToolResults[0].EventID != testIDN(2750).String() ||
		bundle.ToolResults[0].ModelCallContextID != testIDN(749).String() ||
		bundle.ToolResults[0].SourceEventSequence != 149 {
		t.Fatalf("tool result lost its durable source identity: %+v", bundle.ToolResults[0])
	}
	if store.completedToolCallWatermark != 760 {
		t.Fatalf("completed tool calls loaded at watermark %d, want 760", store.completedToolCallWatermark)
	}
	if !strings.Contains(bundle.SystemPrompt, "Help the user make progress.") {
		t.Fatalf("expected system prompt to include agent instruction, got %q", bundle.SystemPrompt)
	}
	agentPublicID, err := publicid.Encode(publicid.KindAgent, testAgentID)
	if err != nil {
		t.Fatalf("encode agent public id: %v", err)
	}
	if !strings.Contains(bundle.SystemPrompt, "Your Omnara agent ID is `"+agentPublicID+"`.") {
		t.Fatalf("expected system prompt to include agent public id, got %q", bundle.SystemPrompt)
	}
	if string(bundle.ToolResults[0].Input) != `{"command":"echo 750"}` {
		t.Fatalf("expected tool input to survive context projection, got %+v", bundle.ToolResults[0])
	}
	serialized := string(contextFixtureJSON(t, bundle))
	for _, internal := range []string{
		"provider_replay",
		"provider_call_id",
		"tool_call_id",
		"durable_id",
		testIDN(750).String(),
		testIDN(760).String(),
		testIDN(1750).String(),
		testIDN(1760).String(),
	} {
		if strings.Contains(serialized, internal) {
			t.Fatalf("generic context JSON must not expose %s internals: %s", internal, serialized)
		}
	}
}

func TestBuildPreservesCanonicalAgentInputTextVerbatim(t *testing.T) {
	store := &fakeContextStore{watermark: 1}
	text := `visible result {"visible":true,"raw":"user raw value","tool_call_id":"tcl_internal",` +
		`"interaction_id":"int_internal","model_call_context_id":"mcc_internal",` +
		`"provider_metadata":{"raw":true},"provider_operation_id":"pop_internal",` +
		`"machine_connection_id":"mcn_internal","machine_connection_generation":7,` +
		`"agent_machine_binding_id":"smb_internal","machine_ref":"mchr-abc234",` +
		`"connector_installation_id":"cin_internal","payload":{"raw":"payload raw value",` +
		`"visible":true,"process_id":"prc_internal","lease_id":"lse_internal"},` +
		`"runtime_lock_id":"lock_internal","turn_id":"Q2"}`
	store.messages = []executionstore.ContextEventRecord{{
		ID:           testIDN(900),
		ProjectID:    testProjectID,
		AgentID:      testAgentID,
		Role:         modelprotocol.RoleUser,
		Sequence:     1,
		ContentParts: contextFixtureJSON(t, []map[string]string{{"type": "text", "text": text}}),
	}}
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(bundle.Messages) != 1 {
		t.Fatalf("expected one message, got %+v", bundle.Messages)
	}
	if string(bundle.Messages[0].Content) != string(store.messages[0].ContentParts) {
		t.Fatalf(
			"model-visible message content changed:\ngot  %s\nwant %s",
			bundle.Messages[0].Content,
			store.messages[0].ContentParts,
		)
	}
}

func TestContextEventsToMessagesPreservesDurableErrorAndReplayMetadata(t *testing.T) {
	inputID := testIDN(905)
	replay := json.RawMessage(`{"format":"openai-responses","payload":[{"type":"reasoning","id":"rs_1"}]}`)
	content := json.RawMessage(`[{"type":"error","text":"provider unavailable","category":"provider_overloaded"}]`)
	messages, err := contextEventsToMessages([]executionstore.ContextEventRecord{{
		ID:                    testIDN(906),
		AgentInputID:          inputID,
		ModelCallContextID:    testIDN(907),
		ModelProviderConfigID: testIDN(908),
		Role:                  modelprotocol.RoleAssistant,
		Sequence:              8,
		ContentParts:          content,
		RequestedModelSlug:    "gpt-test",
		APIFormat:             "openai-responses",
		APIVariant:            "openai",
		ProviderReplay:        replay,
	}})
	if err != nil {
		t.Fatalf("project context events: %v", err)
	}
	if len(messages) != 1 {
		t.Fatalf("messages = %+v, want one", messages)
	}
	message := messages[0]
	if message.AgentInputID != inputID.String() ||
		message.Role != modelprotocol.RoleAssistant ||
		message.Sequence != 8 ||
		string(message.Content) != string(content) ||
		string(message.ProviderReplay) != string(replay) ||
		message.ModelCallContextID != testIDN(907).String() ||
		message.ProviderReplaySource.ModelProviderConfigID != testIDN(908).String() ||
		message.ProviderReplaySource.RequestedProviderModelSlug != "gpt-test" ||
		message.ProviderReplaySource.APIFormat != "openai-responses" ||
		message.ProviderReplaySource.APIVariant != "openai" {
		t.Fatalf("durable error or replay metadata changed during projection: %+v", message)
	}
}

func TestContextEventsToMessagesRejectsMissingRole(t *testing.T) {
	_, err := contextEventsToMessages([]executionstore.ContextEventRecord{{
		ID:           testIDN(909),
		Sequence:     1,
		ContentParts: json.RawMessage(`[{"type":"text","text":"ambiguous"}]`),
	}})
	if err == nil || !strings.Contains(err.Error(), "missing its model role") {
		t.Fatalf("missing role error = %v", err)
	}
}

func TestBuildDoesNotRewriteToolResultContentByFieldName(t *testing.T) {
	store := &fakeContextStore{watermark: 2}
	store.toolCalls = []executionstore.ToolCallRecord{{
		ID:                      testIDN(901),
		ProviderCallID:          "call_provider",
		TurnID:                  testTurnID,
		Name:                    "run_command",
		Input:                   json.RawMessage(`{}`),
		ModelCallContextID:      testIDN(900),
		ToolCallResultID:        testIDN(1901),
		ToolResultEventID:       testIDN(2901),
		SourceEventSequence:     1,
		ToolResultEventSequence: 2,
		State:                   executionstore.ToolCallStateCompleted,
		Outcome:                 executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: contextFixtureJSON(t, []map[string]any{{
			"type": "structured_data",
			"value": map[string]any{
				"visible":                  true,
				"raw":                      "user raw result",
				"runtime_lock_id":          "lock_secret",
				"turn_id":                  "Q2",
				"tool_call_id":             "tcl_internal",
				"interaction_id":           "int_internal",
				"model_call_context_id":    "mcc_internal",
				"provider_metadata":        map[string]any{"raw": true},
				"daemon_instance_id":       "din_secret",
				"agent_machine_binding_id": "smb_internal",
				"payload": map[string]any{
					"lease_id": "lse_secret",
					"raw":      "payload raw result",
					"visible":  true,
				},
			},
		}}),
	}}
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	body := string(contextFixtureJSON(t, bundle))
	for _, expected := range []string{
		`"visible":true`,
		"user raw result",
		"payload raw result",
		"tool_call_id",
		"tcl_internal",
		"interaction_id",
		"int_internal",
		"model_call_context_id",
		"mcc_internal",
		"provider_metadata",
		"daemon_instance_id",
		"din_secret",
		"agent_machine_binding_id",
		"smb_internal",
		"lease_id",
		"lse_secret",
		"runtime_lock_id",
		"lock_secret",
		"turn_id",
		"Q2",
	} {
		if !strings.Contains(body, expected) {
			t.Fatalf("model-visible tool result lost %q: %s", expected, body)
		}
	}
}

func TestBuildPreservesStructuredProcessResultValues(t *testing.T) {
	store := &fakeContextStore{watermark: 2}
	store.toolCalls = []executionstore.ToolCallRecord{{
		ID:                      testIDN(902),
		ProviderCallID:          "call_process",
		TurnID:                  testTurnID,
		Name:                    "run_command",
		Input:                   json.RawMessage(`{}`),
		ModelCallContextID:      testIDN(903),
		ToolCallResultID:        testIDN(1902),
		ToolResultEventID:       testIDN(2902),
		SourceEventSequence:     1,
		ToolResultEventSequence: 2,
		State:                   executionstore.ToolCallStateCompleted,
		Outcome:                 executionstore.ToolResultOutcomeSucceeded,
		ResultContentParts: contextFixtureJSON(t, []map[string]any{{
			"type": "structured_data",
			"value": map[string]any{
				"process_id":            "prc_visible",
				"machine_connection_id": "mcn_internal",
			},
		}}),
	}}
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	body := string(bundle.ToolResults[0].ContentParts)
	if !strings.Contains(body, `"process_id":"prc_visible"`) {
		t.Fatalf("process tool result lost process_id: %s", body)
	}
	if !strings.Contains(body, `"machine_connection_id":"mcn_internal"`) {
		t.Fatalf("process tool result lost machine_connection_id: %s", body)
	}
}

func TestBuildProjectsIntegrationTargets(t *testing.T) {
	targetID := testIDN(940)
	store := &fakeContextStore{
		watermark: 15,
		hasConfig: true,
		config:    testAgentConfigRecord(),
		integrationTargets: []integrationstore.IntegrationTargetSummary{{
			ID:              targetID,
			TargetRef:       "slack-abcd",
			Provider:        integrationstore.IntegrationProviderSlack,
			InstallState:    integrationstore.IntegrationInstallStateDisabled,
			ProviderRef:     "C123:1712345678.000100",
			ProviderRefKind: "thread",
			DisplayName:     "general",
		}},
	}
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(bundle.ToolSpecs) != 1 ||
		bundle.ToolSpecs[0].Name != toolcatalog.ToolNameSendIntegrationMessage ||
		bundle.ToolSpecs[0].Permission.Mode != toolpermission.ModeAlwaysAllow {
		t.Fatalf("expected implicit integration send tool, got %+v", bundle.ToolSpecs)
	}
	if len(bundle.IntegrationTargets) != 1 ||
		bundle.IntegrationTargets[0].TargetRef != "slack-abcd" ||
		bundle.IntegrationTargets[0].IsCurrent {
		t.Fatalf("expected integration target projection, got %+v", bundle.IntegrationTargets)
	}
	if serialized := string(contextFixtureJSON(t, bundle)); strings.Contains(serialized, targetID.String()) {
		t.Fatalf("integration target projection must not expose raw target id: %s", serialized)
	}
	if bundle.IntegrationTargets[0].DurableID != targetID.String() {
		t.Fatalf("integration target missing durable target id: %+v", bundle.IntegrationTargets)
	}
	if bundle.IntegrationTargets[0].Label != "slack thread 1712345678.000100 in C123 (#general)" {
		t.Fatalf("integration target label = %q", bundle.IntegrationTargets[0].Label)
	}
}

func TestImplicitReceiveOnlyChannelDoesNotExposeLegacyIntegrationSend(t *testing.T) {
	contract, err := WithImplicitIntegrationTools(
		agentconfig.RuntimeContract{},
		[]integrationstore.IntegrationTargetSummary{{
			ID:       testIDN(943),
			Provider: integrationstore.IntegrationProviderSlack,
		}},
		integrationstore.AgentChannelToolEligibility{List: true, Send: false},
	)
	if err != nil {
		t.Fatalf("add mixed integration tools: %v", err)
	}
	want := map[string]bool{toolcatalog.ToolNameListChannels: false}
	for _, tool := range contract.Tools {
		if _, expected := want[tool.Name]; expected {
			want[tool.Name] = true
		}
		if tool.Name == toolcatalog.ToolNameSendChannelMessage ||
			tool.Name == toolcatalog.ToolNameSendIntegrationMessage {
			t.Fatalf("receive-only channel unexpectedly enabled %s", tool.Name)
		}
	}
	for name, found := range want {
		if !found {
			t.Fatalf("mixed integration contract is missing %s: %+v", name, contract.Tools)
		}
	}
}

func TestBuildExplicitlyDisabledIntegrationSendToolOverridesImplicitTarget(t *testing.T) {
	result, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(`
instruction: Help the user make progress.
model:
  provider_config: deterministic-test
  name: deterministic-owned-kernel-test
tools:
  ask_question: {}
  send_integration_message:
    enabled: false
`), agentconfig.CompileOptions{})
	if err != nil {
		t.Fatalf("compile disabled integration send config: %v", err)
	}
	store := &fakeContextStore{
		watermark: 15,
		hasConfig: true,
		config: executionstore.AgentConfigRecord{
			ID:                      testIDN(941),
			CompiledDefinition:      json.RawMessage(result.CanonicalJSON),
			CompilerVersion:         agentconfig.CompilerVersion,
			EffectiveDefinitionHash: result.Hash,
		},
		integrationTargets: []integrationstore.IntegrationTargetSummary{{
			ID:        testIDN(942),
			TargetRef: "slack-disabled",
			Provider:  integrationstore.IntegrationProviderSlack,
		}},
	}
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if HasTool(bundle.ToolSpecs, toolcatalog.ToolNameSendIntegrationMessage) {
		t.Fatalf("explicitly disabled integration send tool was exposed: %+v", bundle.ToolSpecs)
	}
	if !HasTool(bundle.ToolSpecs, toolcatalog.ToolNameAskQuestion) {
		t.Fatalf("explicitly enabled ask question tool was not exposed: %+v", bundle.ToolSpecs)
	}
	if len(bundle.IntegrationTargets) != 0 {
		t.Fatalf("integration targets leaked without an integration tool: %+v", bundle.IntegrationTargets)
	}
}

func TestIntegrationTargetLabel(t *testing.T) {
	tests := []struct {
		name   string
		target integrationstore.IntegrationTargetSummary
		want   string
	}{
		{
			name: "slack thread with channel name",
			target: integrationstore.IntegrationTargetSummary{
				Provider:        integrationstore.IntegrationProviderSlack,
				ProviderRefKind: "thread",
				ProviderRef:     "C123:1712345678.000100",
				DisplayName:     "general",
			},
			want: "slack thread 1712345678.000100 in C123 (#general)",
		},
		{
			name: "slack thread without channel name",
			target: integrationstore.IntegrationTargetSummary{
				Provider:        integrationstore.IntegrationProviderSlack,
				ProviderRefKind: "thread",
				ProviderRef:     "C123:1712345678.000100",
			},
			want: "slack thread 1712345678.000100 in C123",
		},
		{
			name: "slack thread with malformed ref",
			target: integrationstore.IntegrationTargetSummary{
				Provider:        integrationstore.IntegrationProviderSlack,
				ProviderRefKind: "thread",
				ProviderRef:     "C123",
			},
			want: "slack thread C123",
		},
		{
			name: "slack dm",
			target: integrationstore.IntegrationTargetSummary{
				Provider:        integrationstore.IntegrationProviderSlack,
				ProviderRefKind: "dm",
				ProviderRef:     "D456",
			},
			want: "slack dm D456",
		},
		{
			name: "unknown ref kind",
			target: integrationstore.IntegrationTargetSummary{
				Provider:        integrationstore.IntegrationProviderSlack,
				ProviderRefKind: "channel",
				ProviderRef:     "C123",
			},
			want: "slack channel C123",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := integrationTargetLabel(test.target); got != test.want {
				t.Fatalf("integrationTargetLabel = %q, want %q", got, test.want)
			}
		})
	}
}

type fakeContextStore struct {
	messages                   []executionstore.ContextEventRecord
	toolCalls                  []executionstore.ToolCallRecord
	completedToolCallWatermark int64
	integrationTargets         []integrationstore.IntegrationTargetSummary
	channelToolEligibility     integrationstore.AgentChannelToolEligibility
	machinePools               []executionstore.MachinePoolSourceRecord
	watermark                  int64
	checkpoints                []executionstore.ContextCheckpointRecord
	config                     executionstore.AgentConfigRecord
	hasConfig                  bool
	noConfig                   bool
	mcpConnections             map[string]executionstore.MCPConnectionRecord
	artifacts                  []artifactstore.ArtifactRecord
	artifactContent            map[string][]byte
	artifactBlobReads          []storage.ID
	skills                     map[string]skillstore.SkillRecord
}

func (s *fakeContextStore) GetAgentChannelToolEligibility(
	context.Context,
	storage.ID,
	storage.ID,
) (integrationstore.AgentChannelToolEligibility, error) {
	return s.channelToolEligibility, nil
}

func (s *fakeContextStore) GetSkillForDispatch(
	_ context.Context,
	_ storage.ID,
	publicSkillID string,
) (skillstore.SkillRecord, error) {
	if record, ok := s.skills[publicSkillID]; ok {
		return record, nil
	}
	return skillstore.SkillRecord{}, storeerr.ErrNotFound
}

func (s *fakeContextStore) ListAgentArtifactsByIDs(
	ctx context.Context,
	projectID, agentID storage.ID,
	ids []storage.ID,
) ([]artifactstore.ArtifactRecord, error) {
	_ = ctx
	_ = projectID
	requested := make(map[storage.ID]bool, len(ids))
	for _, id := range ids {
		requested[id] = true
	}
	var out []artifactstore.ArtifactRecord
	for _, record := range s.artifacts {
		if record.AgentID == agentID && requested[record.ID] {
			out = append(out, record)
		}
	}
	return out, nil
}

func (s *fakeContextStore) GetArtifactBlob(
	ctx context.Context,
	projectID, agentID, id storage.ID,
) ([]byte, artifactstore.ArtifactRecord, error) {
	_ = ctx
	_ = projectID
	s.artifactBlobReads = append(s.artifactBlobReads, id)
	for _, record := range s.artifacts {
		if record.AgentID == agentID && record.ID == id {
			content := s.artifactContent[id.String()]
			if content == nil {
				content = []byte("fake artifact content")
			}
			return content, record, nil
		}
	}
	return nil, artifactstore.ArtifactRecord{}, storeerr.ErrNotFound
}

func (s *fakeContextStore) ListContextEvents(
	ctx context.Context,
	projectID, agentID storage.ID,
	afterSequence int64,
	watermark int64,
	limit int32,
) ([]executionstore.ContextEventRecord, error) {
	_ = ctx
	_ = projectID
	_ = agentID
	_ = watermark
	var out []executionstore.ContextEventRecord
	for _, message := range s.messages {
		if message.Sequence <= afterSequence || message.Sequence > watermark {
			continue
		}
		out = append(out, message)
		if int32(len(out)) == limit {
			break
		}
	}
	return out, nil
}

func (s *fakeContextStore) ListCompletedToolCallsAtWatermark(
	ctx context.Context,
	projectID, agentID storage.ID,
	afterEventSequence int64,
	watermark int64,
) ([]executionstore.ToolCallRecord, error) {
	_ = ctx
	_ = projectID
	_ = agentID
	_ = afterEventSequence
	s.completedToolCallWatermark = watermark
	var out []executionstore.ToolCallRecord
	for _, toolCall := range s.toolCalls {
		if toolCall.SourceEventSequence <= 0 || toolCall.ToolResultEventSequence <= 0 {
			return nil, fmt.Errorf("tool-call fixture %s is missing immutable event chronology", toolCall.ID)
		}
		if toolCall.SourceEventSequence <= afterEventSequence {
			continue
		}
		if toolCall.ToolResultEventSequence > watermark {
			continue
		}
		out = append(out, toolCall)
	}
	return out, nil
}

func (s *fakeContextStore) ListIntegrationTargets(
	ctx context.Context,
	projectID, agentID storage.ID,
) ([]integrationstore.IntegrationTargetSummary, error) {
	_ = ctx
	_ = projectID
	_ = agentID
	return s.integrationTargets, nil
}

func (s *fakeContextStore) ListMachinePoolSources(
	ctx context.Context,
	projectID, agentID, agentConfigID storage.ID,
) ([]executionstore.MachinePoolSourceRecord, error) {
	_ = ctx
	_ = projectID
	_ = agentID
	_ = agentConfigID
	return s.machinePools, nil
}

func (s *fakeContextStore) GetLatestApplicableContextCheckpoint(
	ctx context.Context,
	projectID, agentID storage.ID,
	maxEventSequence int64,
) (executionstore.ContextCheckpointRecord, bool, error) {
	_ = ctx
	_ = projectID
	_ = agentID
	var latest executionstore.ContextCheckpointRecord
	found := false
	for _, checkpoint := range s.checkpoints {
		if checkpoint.CheckpointEventSequence > maxEventSequence {
			continue
		}
		if !found || checkpoint.CheckpointEventSequence > latest.CheckpointEventSequence {
			latest = checkpoint
			found = true
		}
	}
	return latest, found, nil
}

func (s *fakeContextStore) GetAgentConfigForAgent(
	ctx context.Context,
	projectID, agentID storage.ID,
) (executionstore.AgentConfigRecord, bool, error) {
	_ = ctx
	_ = projectID
	_ = agentID
	if s.noConfig {
		return executionstore.AgentConfigRecord{}, false, nil
	}
	if !s.hasConfig {
		return testAgentConfigRecord(), true, nil
	}
	return s.config, s.hasConfig, nil
}

func (s *fakeContextStore) CaptureAgentConfigForModelContext(
	ctx context.Context,
	projectID, agentID storage.ID,
) (executionstore.AgentConfigSnapshotRecord, error) {
	config, found, err := s.GetAgentConfigForAgent(ctx, projectID, agentID)
	if err != nil {
		return executionstore.AgentConfigSnapshotRecord{}, err
	}
	if !found {
		return executionstore.AgentConfigSnapshotRecord{}, storeerr.ErrNotFound
	}
	return executionstore.AgentConfigSnapshotRecord{AgentConfig: config, InputEventSequence: s.watermark}, nil
}

func (s *fakeContextStore) ListAgentMCPConnections(
	ctx context.Context,
	projectID, agentID storage.ID,
) ([]executionstore.MCPConnectionRecord, error) {
	_ = ctx
	_ = projectID
	_ = agentID
	if len(s.mcpConnections) == 0 {
		return nil, nil
	}
	keys := make([]string, 0, len(s.mcpConnections))
	for key := range s.mcpConnections {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]executionstore.MCPConnectionRecord, 0, len(keys))
	for _, key := range keys {
		out = append(out, s.mcpConnections[key])
	}
	return out, nil
}

func (s *fakeContextStore) MaxEventSequence(ctx context.Context, projectID, agentID storage.ID) (int64, error) {
	_ = ctx
	_ = projectID
	_ = agentID
	return s.watermark, nil
}

func TestBuildUsesAgentConfigEnabledToolSpecs(t *testing.T) {
	store := &fakeContextStore{watermark: 1, hasConfig: true, config: testAgentConfigRecordWithTools(t, "run_command")}
	store.messages = append(
		store.messages,
		executionstore.ContextEventRecord{
			ID:           testIDN(930),
			ProjectID:    testProjectID,
			AgentID:      testAgentID,
			Sequence:     1,
			Role:         modelprotocol.RoleUser,
			ContentParts: contextFixtureJSON(t, []map[string]string{{"type": "text", "text": "hi"}}),
		},
	)
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(bundle.ToolSpecs) != 1 || bundle.ToolSpecs[0].Name != "run_command" {
		t.Fatalf("expected only enabled run_command tool from config, got %+v", bundle.ToolSpecs)
	}
}

func TestBuildUsesEffectiveSkillToolConfiguration(t *testing.T) {
	skillID, err := publicid.Encode(publicid.KindSkill, testIDN(934))
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	tests := []struct {
		name           string
		toolConfig     string
		wantTool       bool
		wantCatalog    bool
		wantPermission string
	}{
		{
			name:           "implicit",
			wantTool:       true,
			wantCatalog:    true,
			wantPermission: toolpermission.ModeAlwaysAllow,
		},
		{
			name: "explicitly disabled",
			toolConfig: `
tools:
  skill:
    enabled: false
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			compiled, err := agentconfig.Compile(
				agentconfig.SourceFormatYAML,
				[]byte(`
instruction: Use attached skills.
model:
  provider_config: deterministic-test
  name: deterministic-owned-kernel-test
`+test.toolConfig+`
skills:
  - `+skillID+`
`),
				agentconfig.CompileOptions{
					ResolveSkillID: func(id string) (agentconfig.SkillResolution, error) {
						return agentconfig.SkillResolution{PublicID: id, Name: "pdf-tools"}, nil
					},
				},
			)
			if err != nil {
				t.Fatalf("compile skill config: %v", err)
			}
			store := &fakeContextStore{
				watermark: 1,
				hasConfig: true,
				config: executionstore.AgentConfigRecord{
					ID:                      testIDN(935),
					CompiledDefinition:      compiled.CanonicalJSON,
					CompilerVersion:         agentconfig.CompilerVersion,
					EffectiveDefinitionHash: compiled.Hash,
				},
				skills: map[string]skillstore.SkillRecord{
					skillID: {Name: "pdf-tools", Description: "Work with PDF files."},
				},
				messages: []executionstore.ContextEventRecord{
					contextTextEvent(t, testIDN(936), 1, "hello"),
				},
			}
			bundle, err := (Builder{Store: store, Skills: store}).Build(
				context.Background(),
				BuildInput{
					ProjectID:       testProjectID,
					AgentID:         testAgentID,
					TurnID:          testTurnID,
					OpeningInputIDs: []storage.ID{testInputID},
					Now:             time.Now().UTC(),
				},
			)
			if err != nil {
				t.Fatalf("build context: %v", err)
			}
			if got := HasTool(bundle.ToolSpecs, "skill"); got != test.wantTool {
				t.Fatalf("skill tool present = %t, want %t", got, test.wantTool)
			}
			if test.wantTool && bundle.ToolSpecs[0].Permission.Mode != test.wantPermission {
				t.Fatalf(
					"skill permission = %q, want %q",
					bundle.ToolSpecs[0].Permission.Mode,
					test.wantPermission,
				)
			}
			if got := strings.Contains(bundle.SystemPrompt, "<available_skills>"); got != test.wantCatalog {
				t.Fatalf("skill catalog present = %t, want %t", got, test.wantCatalog)
			}
		})
	}
}

func TestBuildIncludesReadyMCPToolSpecs(t *testing.T) {
	store := &fakeContextStore{
		watermark: 1,
		hasConfig: true,
		config:    testAgentConfigRecordWithMCP(t),
		mcpConnections: map[string]executionstore.MCPConnectionRecord{
			"docs": {
				ID:        testIDN(970),
				AgentID:   testAgentID,
				ServerKey: "docs",
				State:     executionstore.MCPConnectionStateReady,
				ToolsSnapshot: contextFixtureJSON(t, []map[string]any{{
					"name":        "greet",
					"description": "say hello",
					"inputSchema": map[string]any{
						"type": "object",
						"properties": map[string]any{
							"name": map[string]any{"type": "string"},
						},
					},
				}, {
					"name":        "disabled",
					"description": "not exposed",
					"inputSchema": map[string]any{"type": "object"},
				}}),
			},
		},
	}
	store.messages = append(
		store.messages,
		executionstore.ContextEventRecord{
			ID:           testIDN(971),
			ProjectID:    testProjectID,
			AgentID:      testAgentID,
			Sequence:     1,
			Role:         modelprotocol.RoleUser,
			ContentParts: contextFixtureJSON(t, []map[string]string{{"type": "text", "text": "hi"}}),
		},
	)
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(bundle.ToolSpecs) != 1 {
		t.Fatalf("expected one mcp tool spec, got %+v", bundle.ToolSpecs)
	}
	spec := bundle.ToolSpecs[0]
	if spec.Name != "mcp__docs__greet" ||
		spec.Type != toolcatalog.ToolTypeMCP ||
		spec.Permission.Mode != toolpermission.ModeAlwaysAllow ||
		!strings.Contains(spec.Description, "say hello") {
		t.Fatalf("unexpected mcp tool spec: %+v", spec)
	}
}

func TestRuntimeContractToolSpecsRejectDuplicateModelFacingNames(t *testing.T) {
	store := &fakeContextStore{
		mcpConnections: map[string]executionstore.MCPConnectionRecord{
			"docs": {
				AgentID:   testAgentID,
				ServerKey: "docs",
				State:     executionstore.MCPConnectionStateReady,
				ToolsSnapshot: contextFixtureJSON(t, []map[string]any{
					{"name": "search", "inputSchema": map[string]any{"type": "object"}},
					{"name": "search", "inputSchema": map[string]any{"type": "object"}},
				}),
			},
		},
	}
	_, err := RuntimeContractToolSpecs(
		context.Background(),
		store,
		testProjectID,
		testAgentID,
		agentconfig.RuntimeContract{
			MCPServers: []agentconfig.RuntimeMCPServer{{
				ServerKey:      "docs",
				DefaultEnabled: true,
				Permission:     toolpermission.DefaultSelection(toolpermission.ModeAlwaysAllow),
			}},
		},
		time.Time{},
	)
	if err == nil || !strings.Contains(err.Error(), `duplicate model-facing tool name "mcp__docs__search"`) {
		t.Fatalf("error = %v, want duplicate model-facing tool name", err)
	}
}

func TestRuntimeContractToolSpecsDoesNotDuplicateExplicitSkillTool(t *testing.T) {
	skillID, err := publicid.Encode(publicid.KindSkill, testIDN(1037))
	if err != nil {
		t.Fatalf("encode skill id: %v", err)
	}
	compiled, err := agentconfig.Compile(
		agentconfig.SourceFormatYAML,
		[]byte(`
instruction: Use attached skills.
model:
  provider_config: deterministic-test
  name: deterministic-owned-kernel-test
tools:
  skill:
    permission:
      mode: always_ask
skills:
  - `+skillID+`
`),
		agentconfig.CompileOptions{
			ResolveSkillID: func(id string) (agentconfig.SkillResolution, error) {
				return agentconfig.SkillResolution{PublicID: id, Name: "pdf-tools"}, nil
			},
		},
	)
	if err != nil {
		t.Fatalf("compile explicit skill config: %v", err)
	}
	contract, err := agentconfig.RuntimeContractFromCompiled(
		compiled.CanonicalJSON,
		agentconfig.CompilerVersion,
		compiled.Hash,
	)
	if err != nil {
		t.Fatalf("load explicit skill runtime contract: %v", err)
	}
	specs, err := RuntimeContractToolSpecs(
		context.Background(),
		&fakeContextStore{},
		testProjectID,
		testAgentID,
		contract,
		time.Now().UTC(),
	)
	if err != nil {
		t.Fatalf("build runtime tool specs: %v", err)
	}
	if len(specs) != 1 ||
		specs[0].Name != "skill" ||
		specs[0].Permission.Mode != toolpermission.ModeAlwaysAsk ||
		!strings.Contains(specs[0].Description, "available_skills catalog") {
		t.Fatalf("runtime tool specs = %+v, want one explicit skill tool", specs)
	}
}

func TestBuildIncludesAvailableMachinePoolsWhenCreateToolEnabled(t *testing.T) {
	store := &fakeContextStore{
		watermark: 1,
		hasConfig: true,
		config:    testAgentConfigRecordWithTools(t, "create_machine"),
		machinePools: []executionstore.MachinePoolSourceRecord{{
			MachinePoolName: "Build Pool",
			Description:     "Build pool",
		}},
	}
	store.messages = append(
		store.messages,
		executionstore.ContextEventRecord{
			ID:           testIDN(931),
			ProjectID:    testProjectID,
			AgentID:      testAgentID,
			Sequence:     1,
			Role:         modelprotocol.RoleUser,
			ContentParts: contextFixtureJSON(t, []map[string]string{{"type": "text", "text": "hi"}}),
		},
	)
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(bundle.AvailableMachinePools) != 1 {
		t.Fatalf("expected one machine pool, got %+v", bundle.AvailableMachinePools)
	}
	pool := bundle.AvailableMachinePools[0]
	if pool.MachinePoolName != "Build Pool" || pool.Description != "Build pool" {
		t.Fatalf("unexpected machine pool context: %+v", pool)
	}
}

func TestBuildOmitsAvailableMachinePoolsWhenCreateToolDisabled(t *testing.T) {
	store := &fakeContextStore{
		watermark: 1,
		hasConfig: true,
		config:    testAgentConfigRecordWithTools(t, "list_machines"),
		machinePools: []executionstore.MachinePoolSourceRecord{{
			MachinePoolName: "Build Pool",
		}},
	}
	store.messages = append(
		store.messages,
		executionstore.ContextEventRecord{
			ID:           testIDN(932),
			ProjectID:    testProjectID,
			AgentID:      testAgentID,
			Sequence:     1,
			Role:         modelprotocol.RoleUser,
			ContentParts: contextFixtureJSON(t, []map[string]string{{"type": "text", "text": "hi"}}),
		},
	)
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if len(bundle.AvailableMachinePools) != 0 {
		t.Fatalf("expected no machine pools, got %+v", bundle.AvailableMachinePools)
	}
}

func TestBuildRequiresPinnedAgentConfig(t *testing.T) {
	store := &fakeContextStore{watermark: 1, noConfig: true}
	_, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err == nil {
		t.Fatal("expected missing pinned config to fail")
	}
}

func TestBuildUsesLatestApplicableCheckpointAndUnsummarizedTail(t *testing.T) {
	store := &fakeContextStore{watermark: 200, checkpoints: []executionstore.ContextCheckpointRecord{
		{
			ID:                             testIDN(942),
			SummarizedThroughEventSequence: 160, CheckpointEventSequence: 161,
			Summary: "cumulative middle",
		},
		{
			ID:                             testIDN(941),
			SummarizedThroughEventSequence: 80, CheckpointEventSequence: 81,
			Summary: "old",
		},
	}}
	store.messages = append(store.messages,
		contextTextEvent(t, testIDN(50), 50, "covered by old checkpoint"),
		contextTextEvent(t, testIDN(120), 120, "covered by middle checkpoint"),
		contextTextEvent(t, testIDN(170), 170, "latest"),
	)
	store.toolCalls = []executionstore.ToolCallRecord{
		{
			ID:                      testIDN(943),
			TurnID:                  testIDN(944),
			ModelCallContextID:      testIDN(945),
			ProviderCallID:          "covered_call",
			Name:                    "run_command",
			Input:                   json.RawMessage(`{}`),
			ToolCallResultID:        testIDN(946),
			ToolResultEventID:       testIDN(947),
			SourceEventSequence:     120,
			ToolResultEventSequence: 150,
			State:                   executionstore.ToolCallStateCompleted,
			Outcome:                 executionstore.ToolResultOutcomeSucceeded,
			ResultContentParts:      json.RawMessage(`[]`),
		},
		{
			ID:                      testIDN(948),
			TurnID:                  testIDN(949),
			ModelCallContextID:      testIDN(950),
			ProviderCallID:          "retained_prior_turn_call",
			Name:                    "run_command",
			Input:                   json.RawMessage(`{}`),
			ToolCallResultID:        testIDN(951),
			ToolResultEventID:       testIDN(952),
			SourceEventSequence:     170,
			ToolResultEventSequence: 180,
			State:                   executionstore.ToolCallStateCompleted,
			Outcome:                 executionstore.ToolResultOutcomeSucceeded,
			ResultContentParts:      json.RawMessage(`[]`),
		},
	}
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if bundle.ContextCheckpoint == nil ||
		bundle.ContextCheckpoint.ID != testIDN(942).String() ||
		bundle.ContextCheckpoint.SummarizedThroughEventSequence != 160 ||
		bundle.ContextCheckpoint.Summary != "cumulative middle" {
		t.Fatalf("latest applicable checkpoint = %+v", bundle.ContextCheckpoint)
	}
	if len(bundle.Messages) != 1 || bundle.Messages[0].ID != testIDN(170).String() {
		t.Fatalf("expected tail after latest checkpoint, got %+v", bundle.Messages)
	}
	if len(bundle.ToolResults) != 1 ||
		bundle.ToolResults[0].ToolCallID != testIDN(948).String() {
		t.Fatalf("expected only cross-turn tool results after checkpoint, got %+v", bundle.ToolResults)
	}
}

func TestBuildIgnoresCheckpointPublishedAfterFixedFrontier(t *testing.T) {
	store := &fakeContextStore{
		watermark: 150,
		checkpoints: []executionstore.ContextCheckpointRecord{
			{
				ID:                             testIDN(1001),
				SummarizedThroughEventSequence: 80, CheckpointEventSequence: 81,
				Summary: "visible checkpoint",
			},
			{
				ID:                             testIDN(1002),
				SummarizedThroughEventSequence: 140, CheckpointEventSequence: 151,
				Summary: "published after the fixed frontier",
			},
		},
	}
	store.messages = append(store.messages,
		contextTextEvent(t, testIDN(70), 70, "covered by visible checkpoint"),
		contextTextEvent(t, testIDN(120), 120, "visible tail"),
		contextTextEvent(t, testIDN(160), 160, "after fixed frontier"),
	)
	bundle, err := (Builder{Store: store}).Build(
		context.Background(),
		BuildInput{
			ProjectID:       testProjectID,
			AgentID:         testAgentID,
			TurnID:          testTurnID,
			OpeningInputIDs: []storage.ID{testInputID},
			Now:             time.Date(2026, 5, 2, 12, 0, 0, 0, time.UTC),
		},
	)
	if err != nil {
		t.Fatalf("build context: %v", err)
	}
	if bundle.ContextCheckpoint == nil || bundle.ContextCheckpoint.ID != testIDN(1001).String() {
		t.Fatalf("checkpoint at frontier 150 = %+v, want visible checkpoint", bundle.ContextCheckpoint)
	}
	if len(bundle.Messages) != 1 || bundle.Messages[0].Sequence != 120 {
		t.Fatalf("messages at frontier 150 = %+v, want only visible tail", bundle.Messages)
	}
}

func testAgentConfigRecord() executionstore.AgentConfigRecord {
	result, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(`
instruction: Help the user make progress.
model:
  provider_config: deterministic-test
  name: deterministic-owned-kernel-test
`), agentconfig.CompileOptions{})
	if err != nil {
		panic(err)
	}
	return executionstore.AgentConfigRecord{
		ID:                      testIDN(500),
		CompiledDefinition:      json.RawMessage(result.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: result.Hash,
	}
}

func testAgentConfigRecordWithTools(t *testing.T, tools ...string) executionstore.AgentConfigRecord {
	t.Helper()
	source := `
instruction: Help the user make progress.
model:
  provider_config: deterministic-test
  name: deterministic-owned-kernel-test
tools:
`
	for _, name := range tools {
		source += "  " + name + ": {}\n"
	}
	result, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(source), agentconfig.CompileOptions{})
	if err != nil {
		t.Fatalf("compile test config: %v", err)
	}
	return executionstore.AgentConfigRecord{
		ID:                      testIDN(501),
		CompiledDefinition:      json.RawMessage(result.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: result.Hash,
	}
}

func testAgentConfigRecordWithMCP(t *testing.T) executionstore.AgentConfigRecord {
	t.Helper()
	result, err := agentconfig.Compile(agentconfig.SourceFormatYAML, []byte(`
instruction: Help the user make progress.
model:
  provider_config: deterministic-test
  name: deterministic-owned-kernel-test
mcp:
  docs:
    url: https://example.com/mcp
    tools:
      greet:
        enabled: true
        permission:
          mode: always_allow
          parameters: {}
      disabled:
        enabled: false
`), agentconfig.CompileOptions{})
	if err != nil {
		t.Fatalf("compile mcp fixture config: %v", err)
	}
	return executionstore.AgentConfigRecord{
		ID:                      testIDN(980),
		CompiledDefinition:      json.RawMessage(result.CanonicalJSON),
		CompilerVersion:         agentconfig.CompilerVersion,
		EffectiveDefinitionHash: result.Hash,
	}
}
