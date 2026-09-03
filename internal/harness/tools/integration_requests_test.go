package tools

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

func TestResolveIntegrationMessageRequest(t *testing.T) {
	t.Parallel()

	request, err := resolveIntegrationMessageRequest(json.RawMessage(`{"text":" hello "}`))
	if err != nil {
		t.Fatal(err)
	}
	if request.Text != " hello " {
		t.Fatalf("text = %q", request.Text)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"text":" "}`),
		json.RawMessage(`{"text":null}`),
		json.RawMessage(`{"text":"hello","channel":"C123"}`),
		json.RawMessage(`{"text":"hello"} {}`),
	} {
		if _, err := resolveIntegrationMessageRequest(raw); err == nil {
			t.Fatalf("expected invalid integration message request to fail: %s", raw)
		}
	}
	oversized, err := json.Marshal(map[string]string{
		"text": strings.Repeat("x", toolcatalog.MaxChannelMessageTextLength+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveIntegrationMessageRequest(oversized); err == nil {
		t.Fatal("oversized integration message was accepted")
	}
}

func TestResolveChannelMessageRequestBoundsTextBytes(t *testing.T) {
	t.Parallel()
	if _, err := resolveChannelMessageRequest(json.RawMessage(`{"text":"hello"}`)); err != nil {
		t.Fatalf("resolve channel message: %v", err)
	}
	oversized, err := json.Marshal(map[string]string{
		"text": strings.Repeat("x", toolcatalog.MaxChannelMessageTextLength+1),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := resolveChannelMessageRequest(oversized); err == nil {
		t.Fatal("oversized channel message was accepted")
	}
}

func TestResolveIntegrationTargetRequest(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		raw  string
		want string
	}{
		{raw: `{"target_ref":"  SLACK-ABCD  "}`, want: "slack-abcd"},
		{raw: `{"target_ref":"1chat-abcd"}`, want: "1chat-abcd"},
		{raw: `{"target_ref":"chat.sdk-abcd"}`, want: "chat.sdk-abcd"},
		{raw: `{"target_ref":"slack-abcdefghjkmn"}`, want: "slack-abcdefghjkmn"},
	} {
		request, err := resolveIntegrationTargetRequest(json.RawMessage(test.raw))
		if err != nil {
			t.Fatal(err)
		}
		if request.TargetRef != test.want {
			t.Fatalf("target_ref = %q, want %q", request.TargetRef, test.want)
		}
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"target_ref":null}`),
		json.RawMessage(`{"target_ref":"slack-abc1"}`),
		json.RawMessage(`{"target_ref":"slack-abcdefgh"}`),
		json.RawMessage(`{"target_ref":"` + strings.Repeat("a", 129) + `-abcd"}`),
		json.RawMessage(`{"target_ref":"slack-abcd","provider":"slack"}`),
		json.RawMessage(`{"target_ref":"slack-abcd"} {}`),
	} {
		if _, err := resolveIntegrationTargetRequest(raw); err == nil {
			t.Fatalf("expected invalid integration target request to fail: %s", raw)
		}
	}
}
