package httpapi

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	openapispec "github.com/omnara-ai/omnara/api/openapi"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/santhosh-tekuri/jsonschema/v6"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestGeneratedSubscriptionRequestsPreserveConversationAndEventSelection(t *testing.T) {
	// This repository ID exceeds float64's exact integer range.
	const conversation = `{"repository_id":9007199254740993,"pull_request":42}`
	var create openapi.CreateIntegrationSubscriptionRequest
	require.NoError(t, json.Unmarshal(
		[]byte(`{"agent_id":"agent","type":"pull_request","conversation":`+conversation+`}`), &create))
	require.JSONEq(t, conversation, string(create.Conversation))
	require.Nil(t, create.Events, "omitted events select integration defaults")
	require.NoError(t, json.Unmarshal(
		[]byte(`{"agent_id":"agent","type":"pull_request","conversation":`+conversation+`,"events":[]}`), &create))
	require.NotNil(t, create.Events, "explicit empty events must reach validation as empty")
	require.Empty(t, *create.Events)

	var launch openapi.CreateAgentRequest
	require.NoError(t, json.Unmarshal(
		[]byte(`{"config":"config","subscriptions":[{"integration_id":"integration","type":"pull_request","conversation":`+
			conversation+`,"events":["commit"]}]}`), &launch))
	require.NotNil(t, launch.Subscriptions)
	require.Len(t, *launch.Subscriptions, 1)
	attachment := (*launch.Subscriptions)[0]
	require.JSONEq(t, conversation, string(attachment.Conversation))
	require.Equal(t, []string{"commit"}, *attachment.Events)
	raw, err := json.Marshal(attachment)
	require.NoError(t, err)
	require.Contains(t, string(raw), `"repository_id":9007199254740993`)
}

func TestSubscriptionRequestEventSelectionContract(t *testing.T) {
	var decoded any
	require.NoError(t, yaml.Unmarshal(openapispec.YAML, &decoded))
	raw, err := json.Marshal(decoded)
	require.NoError(t, err)
	document, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	require.NoError(t, err)
	compiler := jsonschema.NewCompiler()
	compiler.DefaultDraft(jsonschema.Draft2020)
	const resource = "urn:omnara:subscription-event-contract"
	require.NoError(t, compiler.AddResource(resource, document))

	for _, request := range []struct {
		schema, idKey, idPrefix string
	}{
		{"IntegrationSubscriptionAttachment", "integration_id", "itg_"},
		{"CreateIntegrationSubscriptionRequest", "agent_id", "agt_"},
	} {
		t.Run(request.schema, func(t *testing.T) {
			schema, err := compiler.Compile(resource + "#/components/schemas/" + request.schema)
			require.NoError(t, err)
			for _, tc := range []struct {
				name   string
				events []any
				valid  bool
			}{
				{"omitted uses defaults", nil, true},
				{"one event", []any{"commit"}, true},
				{"distinct events", []any{"commit", "review_comment"}, true},
				{"empty selection", []any{}, false},
				{"duplicate selection", []any{"commit", "commit"}, false},
			} {
				t.Run(tc.name, func(t *testing.T) {
					body := map[string]any{
						request.idKey:  request.idPrefix + strings.Repeat("a", 26),
						"type":         "pull_request",
						"conversation": map[string]any{"repository_id": 123, "pull_request": 42},
					}
					if tc.events != nil {
						body["events"] = tc.events
					}
					err := schema.Validate(body)
					if tc.valid {
						require.NoError(t, err)
					} else {
						require.Error(t, err)
					}
				})
			}
		})
	}
}
