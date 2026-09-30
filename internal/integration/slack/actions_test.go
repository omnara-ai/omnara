package slack

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/stretchr/testify/require"
)

func TestQuestionTextFollowsExplicitOptionSelection(t *testing.T) {
	for _, tc := range []struct {
		name     string
		multiple bool
		selected []string
		text     string
		want     []int
		invalid  bool
	}{
		{"ordinary choice", false, []string{"0"}, "", []int{0}, false},
		{"whitespace is not an answer", false, []string{"0"}, "  ", []int{0}, false},
		{"text ignored for ordinary choice", false, []string{"0"}, "wait for CI", []int{0}, false},
		{"text with Other", false, []string{"1"}, "wait for CI", []int{1}, false},
		{"text without radio choice", false, nil, "wait for CI", nil, true},
		{"multiple preserves choices", true, []string{"0"}, "wait for CI", []int{0}, false},
		{"multiple Other is not duplicated", true, []string{"0", "1"}, "wait for CI", []int{0, 1}, false},
		{"blank without choice", false, nil, "", nil, true},
		{"text does not hide invalid choice", false, []string{"9"}, "wait for CI", nil, true},
		{"text does not hide duplicate choice", true, []string{"0", "0"}, "wait for CI", nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			form := interactionform.Form{Title: "Question", Questions: []interactionform.Question{{
				Prompt: "Deploy?", Multiple: tc.multiple,
				Options: []interactionform.Option{{Label: "Yes"}, {Label: "Other", AllowsText: true}},
			}}}
			choice := actionStateValue{}
			for _, selected := range tc.selected {
				if tc.multiple {
					choice.SelectedOptions = append(choice.SelectedOptions, actionStateOption{Value: selected})
				} else {
					choice.SelectedOption = &actionStateOption{Value: selected}
				}
			}
			result := ResolveInteractionForm(form, ActionState{Values: map[string]map[string]actionStateValue{
				questionBlockID(0):           {PromptAnswerAction: choice},
				questionBlockID(0) + "_text": {PromptAnswerAction: {Value: tc.text}},
			}})
			if tc.invalid {
				require.NotEmpty(t, result.InvalidReason)
				return
			}
			require.Empty(t, result.InvalidReason)
			require.Len(t, result.Resolution.Answers, 1)
			require.Equal(t, tc.want, result.Resolution.Answers[0].OptionIndices)
			wantText := ""
			for _, index := range tc.want {
				if index == 1 {
					wantText = strings.TrimSpace(tc.text)
				}
			}
			require.Equal(t, wantText, result.Resolution.Answers[0].Text)
		})
	}
}

func TestPromptCallbackIdentityDoesNotSubmitFormEdits(t *testing.T) {
	value := PromptActionValue{
		Type: PromptType, InteractionID: "interaction-test", AgentID: "agent-test", IntegrationTargetID: "target-test",
	}
	encoded, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"integration_target_id":"target-test"`) {
		t.Fatalf("stored Slack button target field changed: %s", encoded)
	}
	submit := actionButton{Type: "button", ActionID: PromptAction, Value: string(encoded)}
	for _, element := range []string{"radio_buttons", "checkboxes", "plain_text_input"} {
		t.Run(element, func(t *testing.T) {
			envelope := ActionsEnvelope{Actions: []actionButton{{Type: element, ActionID: PromptAnswerAction}}}
			envelope.Message.Blocks = []HistoryBlock{
				{BlockID: promptMarkerBlockID(value.InteractionID)},
				{Elements: []actionButton{submit}},
			}
			id, err := PromptCallbackInteractionID(envelope)
			if err != nil || id != value.InteractionID {
				t.Fatalf("callback identity = %q, %v", id, err)
			}
			if _, err := PromptActionFromActions(envelope); err == nil {
				t.Fatal("form edit was interpreted as a submitted answer")
			}
		})
	}
	id, err := PromptCallbackInteractionID(ActionsEnvelope{Actions: []actionButton{submit}})
	if err != nil || id != value.InteractionID {
		t.Fatalf("submit identity = %q, %v", id, err)
	}
	for _, blockID := range []string{"", "unrelated", promptMarkerBlockID("")} {
		envelope := ActionsEnvelope{Actions: []actionButton{{ActionID: PromptAnswerAction}}}
		envelope.Message.Blocks = []HistoryBlock{{BlockID: blockID}}
		if _, err := PromptCallbackInteractionID(envelope); err == nil {
			t.Fatalf("accepted missing prompt identity in block %q", blockID)
		}
	}
}

func TestActionsUserDisplayName(t *testing.T) {
	tests := []struct {
		name string
		user ActionsUser
		want string
	}{
		{
			name: "uses name",
			user: ActionsUser{ID: "U123", Name: "Ada", Username: "ada"},
			want: "Ada",
		},
		{
			name: "falls back to username",
			user: ActionsUser{ID: "U123", Username: "ada"},
			want: "ada",
		},
		{
			name: "empty when nothing known",
			user: ActionsUser{ID: "U123"},
			want: "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := test.user.DisplayName(); got != test.want {
				t.Fatalf("DisplayName = %q, want %q", got, test.want)
			}
		})
	}
}

func TestResolveInteractionFormSupportsMultipleSelections(t *testing.T) {
	value := interactionform.Form{
		Title: "Question",
		Questions: []interactionform.Question{{
			Prompt:   "Services?",
			Multiple: true,
			Options: []interactionform.Option{
				{Label: "API"},
				{Label: "Worker", AllowsText: true},
			},
		}},
	}
	result := ResolveInteractionForm(value, ActionState{
		Values: map[string]map[string]actionStateValue{
			questionBlockID(0): {
				PromptAnswerAction: {
					SelectedOptions: []actionStateOption{{Value: "1"}, {Value: "0"}},
				},
			},
			questionBlockID(0) + "_text": {
				PromptAnswerAction: {Value: " explanation "},
			},
		},
	})
	if result.InvalidReason != "" ||
		len(result.Resolution.Answers) != 1 ||
		len(result.Resolution.Answers[0].OptionIndices) != 2 ||
		result.Resolution.Answers[0].OptionIndices[0] != 0 ||
		result.Resolution.Answers[0].OptionIndices[1] != 1 ||
		result.Resolution.Answers[0].Text != "explanation" {
		t.Fatalf("ResolveInteractionForm() = %+v", result)
	}
}
