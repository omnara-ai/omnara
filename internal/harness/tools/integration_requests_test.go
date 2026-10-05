package tools

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestResolveIntegrationMessageRequest(t *testing.T) {
	t.Parallel()

	artifactID, err := publicid.Encode(
		publicid.KindArtifact,
		integrationToolTestID("integration-message-artifact"),
	)
	require.NoError(t, err)
	artifactPath := "/artifacts/" + artifactID
	request, err := resolveIntegrationMessageRequest(
		json.RawMessage(`{"text":" hello ","paths":["` + artifactPath + `","/memory/engineering/reports/a.pdf"]}`),
	)
	require.NoError(t, err)
	if request.Text != " hello " || len(request.Paths) != 2 || request.Paths[0] != artifactPath {
		t.Fatalf("request = %+v", request)
	}
	for _, raw := range []json.RawMessage{
		json.RawMessage(`{"text":" "}`),
		json.RawMessage(`{"text":null}`),
		json.RawMessage(`{"text":"hello","paths":["agt_invalid"]}`),
		json.RawMessage(`{"text":"hello","artifact_id":"` + artifactPath + `"}`),
		json.RawMessage(`{"text":"hello","artifact_ids":["` + artifactID + `"]}`),
		json.RawMessage(`{"text":"hello","paths":["` + artifactID + `"]}`),
		json.RawMessage(`{"text":"hello","paths":["/artifacts"]}`),
		json.RawMessage(`{"text":"hello","paths":["/artifacts/` + artifactID + `/extra"]}`),
		json.RawMessage(`{"text":"hello","paths":["/memory/engineering"]}`),
		json.RawMessage(`{"text":"hello","paths":["/memory/engineering/../secret"]}`),
		json.RawMessage(`{"text":"hello","paths":["/memory/engineering/a/../../secret"]}`),
		json.RawMessage(`{"text":"hello","paths":["/memory/engineering/a/"]}`),
		json.RawMessage(`{"text":"hello","paths":["/skills/example/SKILL.md"]}`),
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
	require.NoError(t, err)
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
