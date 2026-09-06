package tools

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
)

func TestResolveIntegrationMessageRequest(t *testing.T) {
	t.Parallel()

	artifactID, err := publicid.Encode(
		publicid.KindArtifact,
		integrationToolTestID("integration-message-artifact"),
	)
	if err != nil {
		t.Fatal(err)
	}
	request, err := resolveIntegrationMessageRequest(
		json.RawMessage(`{"text":" hello ","artifact_ids":["` + artifactID + `"]}`),
	)
	if err != nil {
		t.Fatal(err)
	}
	if request.Text != " hello " || len(request.ArtifactIDs) != 1 || request.ArtifactIDs[0] != artifactID {
		t.Fatalf("request = %+v", request)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"text":" "}`),
		json.RawMessage(`{"text":null}`),
		json.RawMessage(`{"text":"hello","artifact_ids":["agt_invalid"]}`),
		json.RawMessage(`{"text":"hello","artifact_id":"` + artifactID + `"}`),
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
