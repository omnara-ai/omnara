package interactionform

import (
	"strings"
	"testing"
)

func TestInteractionFormRejectsInvalidQuestions(t *testing.T) {
	for _, test := range []struct {
		name      string
		questions []Question
		want      string
	}{
		{
			name: "missing prompt",
			questions: []Question{{
				Options: []Option{{Label: "Postgres"}},
			}},
			want: "requires a prompt",
		},
		{
			name: "missing options",
			questions: []Question{{
				Prompt: "Database?",
			}},
			want: "requires at least one option",
		},
		{
			name: "missing option label",
			questions: []Question{{
				Prompt:  "Database?",
				Options: []Option{{}},
			}},
			want: "requires a label",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := New("Questions", nil, test.questions)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("New() error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestNormalizeResolutionUsesQuestionOrder(t *testing.T) {
	value := resolutionTestForm(t)
	resolution, err := NormalizeResolution(value, Resolution{Answers: []Answer{
		{OptionIndices: []int{0}},
		{OptionIndices: []int{0}},
	}})
	if err != nil {
		t.Fatalf("NormalizeResolution() error = %v", err)
	}
	if len(resolution.Answers) != 2 {
		t.Fatalf("answers = %+v", resolution.Answers)
	}

	for _, test := range []struct {
		name     string
		response Resolution
	}{
		{
			name: "missing answer",
			response: Resolution{Answers: []Answer{
				{OptionIndices: []int{0}},
			}},
		},
		{
			name: "unknown option",
			response: Resolution{Answers: []Answer{
				{OptionIndices: []int{1}},
				{OptionIndices: []int{0}},
			}},
		},
		{
			name: "negative option",
			response: Resolution{Answers: []Answer{
				{OptionIndices: []int{-1}},
				{OptionIndices: []int{0}},
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := NormalizeResolution(value, test.response); err == nil {
				t.Fatal("NormalizeResolution() error = nil")
			}
		})
	}
}

func TestNormalizeResolutionAllowsTextOnlyForSelectedOption(t *testing.T) {
	value, err := New("Questions", nil, []Question{{
		Prompt: "Database?",
		Options: []Option{
			{Label: "Postgres"},
			{Label: "Other", AllowsText: true},
		},
	}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := NormalizeResolution(value, Resolution{Answers: []Answer{{
		OptionIndices: []int{0},
		Text:          "SQLite",
	}}}); err == nil {
		t.Fatal("NormalizeResolution() accepted text for a closed option")
	}
	resolution, err := NormalizeResolution(value, Resolution{Answers: []Answer{{
		OptionIndices: []int{1},
		Text:          " SQLite ",
	}}})
	if err != nil {
		t.Fatalf("NormalizeResolution() error = %v", err)
	}
	if got := resolution.Answers[0].Text; got != "SQLite" {
		t.Fatalf("normalized text = %q", got)
	}
	if _, err := NormalizeResolution(value, Resolution{Answers: []Answer{{
		OptionIndices: []int{1},
	}}}); err != nil {
		t.Fatalf("NormalizeResolution() rejected omitted optional text: %v", err)
	}
}

func TestNormalizeResolutionValidatesAndOrdersMultipleSelections(t *testing.T) {
	value, err := New("Questions", nil, []Question{{
		Prompt:   "Services?",
		Multiple: true,
		Options: []Option{
			{Label: "API"},
			{Label: "Worker"},
			{Label: "Other", AllowsText: true},
		},
	}})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	resolution, err := NormalizeResolution(value, Resolution{Answers: []Answer{{
		OptionIndices: []int{2, 0},
		Text:          "Scheduler",
	}}})
	if err != nil {
		t.Fatalf("NormalizeResolution() error = %v", err)
	}
	if got := resolution.Answers[0].OptionIndices; len(got) != 2 || got[0] != 0 || got[1] != 2 {
		t.Fatalf("normalized option indices = %v", got)
	}
	if _, err := NormalizeResolution(value, Resolution{Answers: []Answer{{
		OptionIndices: []int{0, 0},
	}}}); err == nil {
		t.Fatal("NormalizeResolution() accepted a duplicate selection")
	}

	single := resolutionTestForm(t)
	if _, err := NormalizeResolution(single, Resolution{Answers: []Answer{
		{OptionIndices: []int{0, 0}},
		{OptionIndices: []int{0}},
	}}); err == nil {
		t.Fatal("NormalizeResolution() accepted multiple selections for a single-select question")
	}
}

func resolutionTestForm(t *testing.T) Form {
	t.Helper()
	value, err := New("Questions", nil, []Question{
		{
			Prompt:  "Database?",
			Options: []Option{{Label: "Postgres"}},
		},
		{
			Prompt:  "Region?",
			Options: []Option{{Label: "US"}},
		},
	})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	return value
}
