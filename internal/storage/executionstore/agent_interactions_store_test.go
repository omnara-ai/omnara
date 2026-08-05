package executionstore

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

func TestPermissionInteractionResolutionDelegatesToPermissionMode(t *testing.T) {
	request := permissionRequestForStorageTest(t, "test_tool")
	for _, test := range []struct {
		name         string
		state        AgentInteractionState
		resolution   interactionform.Resolution
		wantDecision toolpermission.Decision
	}{
		{
			name:  "allowed",
			state: AgentInteractionStateResolved,
			resolution: interactionform.Resolution{Answers: []interactionform.Answer{{
				OptionIndices: []int{toolpermission.AllowOptionIndex},
			}}},
			wantDecision: toolpermission.DecisionAllow,
		},
		{
			name:  "denied",
			state: AgentInteractionStateResolved,
			resolution: interactionform.Resolution{Answers: []interactionform.Answer{{
				OptionIndices: []int{toolpermission.DenyOptionIndex},
				Text:          "not allowed",
			}}},
			wantDecision: toolpermission.DecisionDeny,
		},
		{
			name:         "canceled",
			state:        AgentInteractionStateCanceled,
			wantDecision: toolpermission.DecisionDeny,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			resolution, err := json.Marshal(test.resolution)
			if err != nil {
				t.Fatalf("marshal resolution: %v", err)
			}
			record := AgentInteractionRecord{
				InteractionKind: "permission",
				State:           test.state,
				Request:         request,
				Resolution:      resolution,
			}
			decision, err := permissionInteractionResolution(record)
			if err != nil {
				t.Fatalf("permissionInteractionResolution() error = %v", err)
			}
			if decision.Decision != test.wantDecision {
				t.Fatalf("decision = %q, want %q", decision.Decision, test.wantDecision)
			}
		})
	}
}

func TestQuestionToolResultKeepsPositionalIdentityForDuplicateLabels(t *testing.T) {
	result := newQuestionToolResult(
		interactionform.Form{Questions: []interactionform.Question{{
			Prompt:   "Choose",
			Multiple: true,
			Options: []interactionform.Option{
				{Label: "Other"},
				{Label: "Other", AllowsText: true},
			},
		}}},
		interactionform.Resolution{Answers: []interactionform.Answer{{
			OptionIndices: []int{0, 1},
		}}},
	)
	if len(result.Answers) != 1 || result.Answers[0].QuestionIndex != 0 {
		t.Fatalf("question result = %+v", result)
	}
	selected := result.Answers[0].SelectedOptions
	if len(selected) != 2 ||
		selected[0].OptionIndex != 0 ||
		selected[1].OptionIndex != 1 ||
		selected[0].Label != "Other" ||
		selected[1].Label != "Other" {
		t.Fatalf("selected options = %+v", selected)
	}
}

func permissionRequestForStorageTest(t *testing.T, toolName string) json.RawMessage {
	t.Helper()
	authorization, err := toolpermission.NewAuthorization(
		toolName,
		json.RawMessage(`{}`),
	)
	if err != nil {
		t.Fatalf("authorization: %v", err)
	}
	mode, ok := toolpermission.FindMode(
		toolpermission.CommonModeDescriptors(),
		toolpermission.ModeAlwaysAsk,
	)
	if !ok {
		t.Fatal("always_ask mode missing")
	}
	value, err := toolpermission.NewAllowDenyForm("Permission requested", nil)
	if err != nil {
		t.Fatalf("interaction form: %v", err)
	}
	request, err := toolpermission.NewRequest(
		mode,
		toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
		authorization,
		value,
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	raw, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}
	return raw
}
