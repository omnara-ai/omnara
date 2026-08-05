package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestConversationURI(t *testing.T) {
	tests := []struct {
		name        string
		workspaceID string
		providerRef string
		want        string
	}{
		{
			name:        "thread",
			workspaceID: "T123",
			providerRef: "C123:1712345678.000100",
			want:        "https://slack.com/app_redirect?channel=C123&team=T123",
		},
		{
			name:        "direct message conversation",
			workspaceID: "T123",
			providerRef: "D123",
			want:        "https://slack.com/app_redirect?channel=D123&team=T123",
		},
		{
			name:        "encodes values",
			workspaceID: "T 123",
			providerRef: "C 123:1712345678.000100",
			want:        "https://slack.com/app_redirect?channel=C+123&team=T+123",
		},
		{
			name:        "missing workspace",
			workspaceID: "",
			providerRef: "C123:1712345678.000100",
			want:        "",
		},
		{
			name:        "missing conversation",
			workspaceID: "T123",
			providerRef: ":1712345678.000100",
			want:        "",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := ConversationURI(test.workspaceID, test.providerRef); got != test.want {
				t.Fatalf("ConversationURI() = %q, want %q", got, test.want)
			}
		})
	}
}

func TestReconcileMessagePaginatesReadback(t *testing.T) {
	tests := []struct {
		name       string
		target     MessageTarget
		wantMethod string
	}{
		{
			name:       "channel",
			target:     MessageTarget{TargetRef: "slack-channel", Channel: "C123", BotToken: "xoxb-test"},
			wantMethod: "/conversations.history",
		},
		{
			name: "thread",
			target: MessageTarget{
				TargetRef: "slack-thread",
				Channel:   "C123",
				ThreadTS:  "111.222",
				BotToken:  "xoxb-test",
			},
			wantMethod: "/conversations.replies",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != test.wantMethod {
					t.Fatalf("path = %q, want %q", r.URL.Path, test.wantMethod)
				}
				if err := r.ParseForm(); err != nil {
					t.Fatalf("parse form: %v", err)
				}
				if r.Form.Get("limit") != "100" {
					t.Fatalf("limit = %q, want 100", r.Form.Get("limit"))
				}
				if test.target.ThreadTS != "" && r.Form.Get("ts") != test.target.ThreadTS {
					t.Fatalf("thread timestamp = %q, want %q", r.Form.Get("ts"), test.target.ThreadTS)
				}
				if requests == 1 {
					writeSlackTestJSON(w, map[string]any{
						"ok":       true,
						"messages": []any{},
						"response_metadata": map[string]any{
							"next_cursor": "page-2",
						},
					})
					return
				}
				if r.Form.Get("cursor") != "page-2" {
					t.Fatalf("cursor = %q, want page-2", r.Form.Get("cursor"))
				}
				writeSlackTestJSON(w, map[string]any{
					"ok": true,
					"messages": []any{map[string]any{
						"ts": "222.333",
						"metadata": map[string]any{
							"event_type": MessageMarkerEventType,
							"event_payload": map[string]any{
								"agent_id":         "agt-test",
								"provider_call_id": "call-test",
								"target_ref":       test.target.TargetRef,
							},
						},
					}},
				})
			}))
			defer server.Close()

			messageID, found, result, err := ReconcileMessage(
				context.Background(),
				slackTestClient(server),
				test.target,
				"agt-test",
				"call-test",
				time.Unix(1_700_000_000, 0),
			)
			if err != nil {
				t.Fatalf("reconcile message: %v", err)
			}
			if !found || messageID != "C123:222.333" || result.DeliveryUnknown {
				t.Fatalf("reconcile result message=%q found=%v result=%+v", messageID, found, result)
			}
			if requests != 2 {
				t.Fatalf("requests = %d, want 2", requests)
			}
		})
	}
}

func TestReconcileMessageReturnsUnknownAtPageLimit(t *testing.T) {
	targets := []struct {
		name   string
		target MessageTarget
		path   string
	}{
		{
			name:   "channel",
			target: MessageTarget{TargetRef: "slack-channel", Channel: "C123", BotToken: "xoxb-test"},
			path:   "/conversations.history",
		},
		{
			name: "thread",
			target: MessageTarget{
				TargetRef: "slack-thread",
				Channel:   "C123",
				ThreadTS:  "111.222",
				BotToken:  "xoxb-test",
			},
			path: "/conversations.replies",
		},
	}
	for _, test := range targets {
		t.Run(test.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				if r.URL.Path != test.path {
					t.Fatalf("path = %q, want %q", r.URL.Path, test.path)
				}
				writeSlackTestJSON(w, map[string]any{
					"ok":       true,
					"messages": []any{},
					"response_metadata": map[string]any{
						"next_cursor": "more",
					},
				})
			}))
			defer server.Close()

			messageID, found, result, err := ReconcileMessage(
				context.Background(),
				slackTestClient(server),
				test.target,
				"agt-test",
				"call-test",
				time.Unix(1_700_000_000, 0),
			)
			if err != nil {
				t.Fatalf("reconcile message: %v", err)
			}
			if found || messageID != "" || !result.DeliveryUnknown {
				t.Fatalf("reconcile result message=%q found=%v result=%+v", messageID, found, result)
			}
			if requests != readbackMaxPages {
				t.Fatalf("requests = %d, want %d", requests, readbackMaxPages)
			}
		})
	}
}
