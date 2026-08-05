package tools

import (
	"encoding/json"
	"testing"
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
}

func TestResolveIntegrationTargetRequest(t *testing.T) {
	t.Parallel()

	request, err := resolveIntegrationTargetRequest(
		json.RawMessage(`{"target_ref":"  SLACK-ABCD  "}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.TargetRef != "slack-abcd" {
		t.Fatalf("target_ref = %q", request.TargetRef)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"target_ref":null}`),
		json.RawMessage(`{"target_ref":"slack-abc1"}`),
		json.RawMessage(`{"target_ref":"slack-abcd","provider":"slack"}`),
		json.RawMessage(`{"target_ref":"slack-abcd"} {}`),
	} {
		if _, err := resolveIntegrationTargetRequest(raw); err == nil {
			t.Fatalf("expected invalid integration target request to fail: %s", raw)
		}
	}
}
