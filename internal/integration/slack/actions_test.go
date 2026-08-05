package slack

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

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
				{Label: "Worker"},
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
		},
	})
	if result.InvalidReason != "" ||
		len(result.Resolution.Answers) != 1 ||
		len(result.Resolution.Answers[0].OptionIndices) != 2 ||
		result.Resolution.Answers[0].OptionIndices[0] != 0 ||
		result.Resolution.Answers[0].OptionIndices[1] != 1 {
		t.Fatalf("ResolveInteractionForm() = %+v", result)
	}
}
