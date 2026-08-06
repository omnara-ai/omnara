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
					t.Fatalf("unexpected path %s", r.URL.Path)
				}
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse readback request: %v", err)
				}
				wantCursor := ""
				if calls > 0 {
					wantCursor = fmt.Sprintf("cursor-%d", calls)
				}
				if r.Form.Get("channel") != "C123" || r.Form.Get("ts") != test.wantFormTS ||
					r.Form.Get("cursor") != wantCursor || r.Form.Get("oldest") != "" ||
					r.Form.Get("limit") != "100" {
					t.Fatalf("readback form=%v", r.Form)
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

			found, result, err := ReconcilePrompt(
				t.Context(),
				slackTestClient(server),
				MessageTarget{Channel: "C123", ThreadTS: test.threadTS, BotToken: "xoxb-test"},
				"interaction-question",
			)
			if err != nil || found || !result.DeliveryUnknown {
				t.Fatalf("reconcile prompt found=%v result=%+v err=%v", found, result, err)
			}
			if calls != readbackMaxPages {
				t.Fatalf("readback calls=%d want %d", calls, readbackMaxPages)
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
	if blocks[1]["block_id"] != "omnara_question_interaction-permission_0" ||
		blocks[2]["block_id"] != "omnara_question_interaction-permission_1" {
		t.Fatalf("question blocks = %#v, %#v", blocks[1], blocks[2])
	}
	elements, ok := blocks[3]["elements"].([]map[string]any)
	if !ok || len(elements) != 1 ||
		elements[0]["action_id"] != PromptAction+"_interaction-permission" {
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

func TestDismissInteractionPromptsRemovesOnlyCanceledQuestionAndPermissionActions(t *testing.T) {
	t.Parallel()
	permissionID := "interaction-permission"
	questionID := "interaction-question"
	staleID := "interaction-stale"
	actionValue := func(interactionID string) string {
		body, err := json.Marshal(PromptActionValue{
			Type:                PromptType,
			InteractionID:       interactionID,
			AgentID:             "agent-123",
			IntegrationTargetID: "integration-123",
		})
		if err != nil {
			t.Fatalf("marshal prompt action value: %v", err)
		}
		return string(body)
	}
	updates := make([]map[string]any, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.replies":
			if err := r.ParseForm(); err != nil {
				t.Fatalf("parse history request: %v", err)
			}
			if r.Form.Get("channel") != "C123" || r.Form.Get("ts") != "111.222" ||
				r.Form.Get("latest") != "333.444" || r.Form.Get("inclusive") != "false" {
				t.Fatalf("history form=%v", r.Form)
			}
			writeSlackTestJSON(w, map[string]any{
				"ok": true,
				"messages": []any{
					map[string]any{
						"text": "Permission requested",
						"ts":   "111.333",
						"blocks": []any{map[string]any{
							"type": "actions",
							"elements": []any{
								map[string]string{
									"action_id": PromptAction,
									"value":     actionValue(permissionID),
								},
								map[string]string{
									"action_id": PromptAction,
									"value":     actionValue(permissionID),
								},
							},
						}},
					},
					map[string]any{
						"text": "What now?",
						"ts":   "222.333",
						"blocks": []any{map[string]any{
							"type": "actions",
							"elements": []any{map[string]string{
								"action_id": PromptAction,
								"value":     actionValue(questionID),
							}},
						}},
					},
					map[string]any{
						"text": "Old answered question?",
						"ts":   "222.444",
						"blocks": []any{map[string]any{
							"type": "actions",
							"elements": []any{map[string]string{
								"action_id": PromptAction,
								"value":     actionValue(staleID),
							}},
						},
						},
					},
				},
			})
		case "/chat.update":
			var update map[string]any
			if err := json.NewDecoder(r.Body).Decode(&update); err != nil {
				t.Fatalf("decode chat update: %v", err)
			}
			updates = append(updates, update)
			if len(updates) == 1 {
				writeSlackTestJSON(w, map[string]any{
					"ok":    false,
					"error": "message_not_found",
				})
				return
			}
			writeSlackTestJSON(w, map[string]any{"ok": true})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := DismissInteractionPrompts(
		t.Context(),
		OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		"xoxb-test",
		Event{Channel: "C123", TS: "333.444", ThreadTS: "111.222"},
		[]string{permissionID, questionID},
	)
	if err != nil || !result.PermanentFailure || result.ProviderCode != "message_not_found" {
		t.Fatalf("dismiss prompts result=%+v err=%v", result, err)
	}
	if len(updates) != 2 {
		t.Fatalf("chat updates=%d want 2", len(updates))
	}
	for i, wantTS := range []string{"111.333", "222.333"} {
		blocks, _ := updates[i]["blocks"].([]any)
		if updates[i]["channel"] != "C123" || updates[i]["ts"] != wantTS ||
			updates[i]["as_user"] != true || len(blocks) != 2 {
			t.Fatalf("chat update=%v", updates[i])
		}
		first, firstOK := blocks[0].(map[string]any)
		second, secondOK := blocks[1].(map[string]any)
		if !firstOK || !secondOK || first["type"] != "section" || second["type"] != "context" {
			t.Fatalf("chat update=%v", updates[i])
		}
	}
}

func TestDismissInteractionPromptsReturnsEmbeddedRateLimit(t *testing.T) {
	t.Parallel()
	_, blocks := InteractionFormPromptBlocks(
		interactionform.Form{
			Title: "Permission requested",
			Questions: []interactionform.Question{{
				Prompt:  "Allow?",
				Options: []interactionform.Option{{Label: "Allow"}},
			}},
		},
		PromptActionValue{
			Type:                PromptType,
			InteractionID:       "interaction-permission",
			AgentID:             "agent-123",
			IntegrationTargetID: "integration-123",
		},
	)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/conversations.history":
			writeSlackTestJSON(w, map[string]any{
				"ok": true,
				"messages": []any{map[string]any{
					"text":   "Permission requested",
					"ts":     "111.333",
					"blocks": blocks,
				}},
			})
		case "/chat.update":
			writeSlackTestJSON(w, map[string]any{"ok": false, "error": "ratelimited"})
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
	}))
	defer server.Close()

	result, err := DismissInteractionPrompts(
		t.Context(),
		OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		"xoxb-test",
		Event{Channel: "C123", TS: "222.333"},
		[]string{"interaction-permission"},
	)
	if err != nil || !result.RateLimited || result.ProviderCode != "ratelimited" {
		t.Fatalf("dismiss prompts result=%+v err=%v", result, err)
	}
}
