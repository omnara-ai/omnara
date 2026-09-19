package slack

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

func TestReconcilePromptBoundsPagination(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name       string
		path       string
		threadTS   string
		wantFormTS string
	}{
		{name: "channel", path: "/conversations.history"},
		{
			name:       "thread",
			path:       "/conversations.replies",
			threadTS:   "111.222",
			wantFormTS: "111.222",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != test.path {
					t.Errorf("unexpected path %s", r.URL.Path)
					http.Error(w, "test handler failed", http.StatusInternalServerError)
					return
				}
				if err := r.ParseForm(); err != nil {
					t.Errorf("parse readback request: %v", err)
					http.Error(w, "test handler failed", http.StatusInternalServerError)
					return
				}
				wantCursor := ""
				if calls > 0 {
					wantCursor = fmt.Sprintf("cursor-%d", calls)
				}
				if r.Form.Get("channel") != "C123" || r.Form.Get("ts") != test.wantFormTS ||
					r.Form.Get("cursor") != wantCursor || r.Form.Get("oldest") != "" ||
					r.Form.Get("limit") != "100" {
					t.Errorf("readback form=%v", r.Form)
					http.Error(w, "test handler failed", http.StatusInternalServerError)
					return
				}
				calls++
				writeSlackTestJSON(w, map[string]any{
					"ok":       true,
					"messages": []any{},
					"response_metadata": map[string]any{
						"next_cursor": fmt.Sprintf("cursor-%d", calls),
					},
				})
			}))
			defer server.Close()

			messageID, result, err := ReconcilePromptReceipt(
				t.Context(),
				slackTestClient(server),
				MessageTarget{Channel: "C123", ThreadTS: test.threadTS, BotToken: "xoxb-test"},
				"interaction-question",
			)
			if err != nil || messageID != "" || !result.DeliveryUnknown {
				t.Fatalf("reconcile prompt messageID=%q result=%+v err=%v", messageID, result, err)
			}
			if calls != readbackMaxPages {
				t.Fatalf("readback calls=%d want %d", calls, readbackMaxPages)
			}
		})
	}
}

func TestDismissPromptUsesConfirmedReceiptWithoutHistory(t *testing.T) {
	t.Parallel()
	for _, providerError := range []string{"", "ratelimited", "message_not_found"} {
		t.Run(providerError, func(t *testing.T) {
			t.Parallel()
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/chat.update" || r.Method != http.MethodPost ||
					r.Header.Get("Authorization") != "Bearer xoxb-test" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.Error(w, "unexpected request", http.StatusBadRequest)
					return
				}
				var body struct {
					Channel string            `json:"channel"`
					TS      string            `json:"ts"`
					Blocks  []json.RawMessage `json:"blocks"`
				}
				if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
					t.Errorf("invalid request: %v", err)
					http.Error(w, "invalid request", http.StatusBadRequest)
					return
				}
				if body.Channel != "C123" || body.TS != "222.333" || body.Blocks == nil || len(body.Blocks) != 0 {
					t.Errorf("dismiss payload = %+v", body)
				}
				writeSlackTestJSON(w, map[string]any{"ok": providerError == "", "error": providerError})
			}))
			defer server.Close()
			result, err := DismissPrompt(t.Context(), slackTestClient(server),
				MessageTarget{Channel: "C123", BotToken: "xoxb-test"}, "222.333", "Response recorded.")
			if err != nil || calls != 1 || result.ProviderCode != providerError ||
				result.RateLimited != (providerError == "ratelimited") ||
				result.PermanentFailure != (providerError == "message_not_found") {
				t.Fatalf("dismiss calls=%d result=%+v err=%v", calls, result, err)
			}
		})
	}
}

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
	if len(blocks) != 4 {
		t.Fatalf("interaction form blocks = %d, want interactive question", len(blocks))
	}
}
