package slack

import (
	"fmt"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

func TestInteractionFormPromptBlocksUsesOneAtomicSubmission(t *testing.T) {
	t.Parallel()
	value := interactionform.Form{
		Title: "Questions",
		Questions: []interactionform.Question{
			{
				Prompt:  "Database?",
				Options: []interactionform.Option{{Label: "Postgres"}},
			},
			{
				Prompt:  "Region?",
				Options: []interactionform.Option{{Label: "US"}},
			},
		},
	}
	summary, blocks := InteractionFormPromptBlocks(
		value,
		PromptActionValue{
			Type:                PromptType,
			InteractionID:       "interaction-permission",
			AgentID:             "agent-123",
			IntegrationTargetID: "integration-123",
		},
	)
	if len(blocks) != 4 {
		t.Fatalf("interaction form blocks = %d, want 4", len(blocks))
	}
	for _, want := range []string{
		"1. Database?",
		"1. Postgres",
		"2. Region?",
		"1. US",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("interaction summary %q does not contain %q", summary, want)
		}
	}
	if blocks[1]["block_id"] != "omnara_question_0" ||
		blocks[2]["block_id"] != "omnara_question_1" {
		t.Fatalf("question blocks = %#v, %#v", blocks[1], blocks[2])
	}
	elements, ok := blocks[3]["elements"].([]map[string]any)
	if !ok || len(elements) != 1 || elements[0]["action_id"] != PromptAction {
		t.Fatalf("submit actions = %#v", blocks[3]["elements"])
	}
}

func TestInteractionFormPromptBlocksFallbackIncludesQuestionsAndOptions(t *testing.T) {
	t.Parallel()
	options := make([]interactionform.Option, 11)
	for index := range options {
		options[index] = interactionform.Option{Label: fmt.Sprintf("Choice %d", index)}
	}
	summary, blocks := InteractionFormPromptBlocks(
		interactionform.Form{
			Title:   "Question",
			Context: []interactionform.ContextItem{{Label: "Repository", Value: "omnara"}},
			Questions: []interactionform.Question{{
				Prompt:  "Which choice?",
				Options: options,
			}},
		},
		PromptActionValue{InteractionID: "interaction-question"},
	)
	if len(blocks) != 2 {
		t.Fatalf("fallback blocks = %d, want 2", len(blocks))
	}
	for _, want := range []string{
		"Repository: omnara",
		"1. Which choice?",
		"1. Choice 0",
		"11. Choice 10",
	} {
		if !strings.Contains(summary, want) {
			t.Fatalf("interaction summary %q does not contain %q", summary, want)
		}
	}
	first, ok := blocks[0]["text"].(map[string]any)
	if !ok || first["text"] != summary {
		t.Fatalf("fallback prompt = %#v, want complete summary", blocks[0])
	}
}

func TestInteractionFormPromptBlocksUsesCheckboxesForMultipleSelection(t *testing.T) {
	t.Parallel()
	_, blocks := InteractionFormPromptBlocks(
		interactionform.Form{
			Title: "Question",
			Questions: []interactionform.Question{{
				Prompt:   "Services?",
				Multiple: true,
				Options: []interactionform.Option{
					{Label: "API"},
					{Label: "Worker"},
				},
			}},
		},
		PromptActionValue{InteractionID: "interaction-question"},
	)
	element, ok := blocks[1]["element"].(map[string]any)
	if !ok || element["type"] != "checkboxes" {
		t.Fatalf("question element = %#v", blocks[1]["element"])
	}
}

func TestInteractionFormPromptBlocksHonorsSlackTextLimits(t *testing.T) {
	t.Parallel()
	_, blocks := InteractionFormPromptBlocks(
		interactionform.Form{
			Title: "Questions",
			Questions: []interactionform.Question{{
				Prompt: strings.Repeat("q", promptInputLabelLimit+1),
				Options: []interactionform.Option{{
					Label: strings.Repeat("o", promptOptionTextLimit+1),
				}},
			}},
		},
		PromptActionValue{
			Type:                PromptType,
			InteractionID:       "interaction-question",
			AgentID:             "agent-123",
			IntegrationTargetID: "integration-123",
		},
	)
	labelObject, labelOK := blocks[1]["label"].(map[string]any)
	label, labelTextOK := labelObject["text"].(string)
	element, elementOK := blocks[1]["element"].(map[string]any)
	options, optionsOK := element["options"].([]map[string]any)
	if !labelOK || !labelTextOK || !elementOK || !optionsOK || len(options) != 1 {
		t.Fatalf("question block = %#v", blocks[1])
	}
	optionObject, optionOK := options[0]["text"].(map[string]any)
	optionText, optionTextOK := optionObject["text"].(string)
	if !optionOK || !optionTextOK {
		t.Fatalf("question option = %#v", options[0])
	}
	if len([]rune(label)) != promptInputLabelLimit ||
		len([]rune(optionText)) != promptOptionTextLimit {
		t.Fatalf("Slack labels have lengths %d and %d", len([]rune(label)), len([]rune(optionText)))
	}
}

func TestInteractionFormPromptBlocksAllowsOptionsWithOptionalText(t *testing.T) {
	t.Parallel()
	_, blocks := InteractionFormPromptBlocks(
		interactionform.Form{
			Title: "Question",
			Questions: []interactionform.Question{{
				Prompt: "Deploy?",
				Options: []interactionform.Option{{
					Label:      "Other",
					AllowsText: true,
				}},
			}},
		},
		PromptActionValue{InteractionID: "interaction-question"},
	)
	if len(blocks) != 3 {
		t.Fatalf("interaction form blocks = %d, want interactive question", len(blocks))
	}
}
