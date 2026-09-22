package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestAppCatalogStaticArgumentSchemas(t *testing.T) {
	response, err := (strictOpenAPIServer{}).ListAppDefinitions(t.Context(), openapi.ListAppDefinitionsRequestObject{})
	require.NoError(t, err)
	catalog, ok := response.(openapi.ListAppDefinitions200JSONResponse)
	require.True(t, ok)
	require.Len(t, catalog.Data, 3)
	toolCount := 0
	for _, definition := range catalog.Data {
		toolCount += len(definition.Capabilities.Tools)
		t.Run(string(definition.AppType), func(t *testing.T) {
			var destination json.RawMessage
			var destinationFields []string
			switch appdefinition.Type(definition.AppType) {
			case appdefinition.SlackThread:
				destination = json.RawMessage(`{"channel_id":"C123","thread_ts":"111.222"}`)
				destinationFields = []string{"channel_id", "thread_ts"}
			case appdefinition.DiscordThread:
				destination = json.RawMessage(`{"channel_id":"123","thread_id":"456","guild_id":"789"}`)
				destinationFields = []string{"channel_id", "thread_id", "guild_id"}
			case appdefinition.GitHubPR:
				destination = json.RawMessage(`{"repository_id":9007199254740993,"pull_request":42}`)
				destinationFields = []string{"repository_id", "pull_request"}
			default:
				t.Fatalf("unexpected app %q", definition.AppType)
			}
			for operation, tool := range definition.Capabilities.Tools {
				require.NotEmpty(t, tool.Description)
				properties, ok := tool.InputSchema["properties"].(map[string]any)
				require.True(t, ok)
				for _, field := range destinationFields {
					require.NotContains(t, properties, field, operation)
				}
			}
			read, err := json.Marshal(definition.Capabilities.Tools["read"].InputSchema)
			require.NoError(t, err)
			require.NoError(t, jsonschema.Validate(read, json.RawMessage(`{}`)))
			require.Error(t, jsonschema.Validate(read, destination), "tools use their assigned conversation")
			require.NotEmpty(t, definition.Capabilities.Subscriptions)
			for _, subscription := range definition.Capabilities.Subscriptions {
				schema, err := json.Marshal(subscription.ConversationSchema)
				require.NoError(t, err)
				require.NoError(t, jsonschema.Validate(schema, destination))
				require.Error(t, jsonschema.Validate(schema, json.RawMessage(`{}`)))
				require.NotEmpty(t, subscription.Events)
			}
			if appdefinition.Type(definition.AppType) == appdefinition.GitHubPR {
				require.Nil(t, definition.Capabilities.InteractionHandler)
				require.Nil(t, definition.Capabilities.Schedule)
				return
			}
			require.NotNil(t, definition.Capabilities.Schedule)
			scheduleSchema, err := json.Marshal(definition.Capabilities.Schedule.InputSchema)
			require.NoError(t, err)
			channel := "C123"
			if appdefinition.Type(definition.AppType) == appdefinition.DiscordThread {
				channel = "123"
			}
			settings, err := json.Marshal(map[string]string{
				"agent_profile_id": "aprf_aaaaaaaaaaaaaaaaaaaaaaaaaa", "channel_id": channel,
				"opening_message_template": "Daily", "message_template": "Prepare the report.",
			})
			require.NoError(t, err)
			require.NoError(t, jsonschema.Validate(scheduleSchema, settings))
			require.Error(t, jsonschema.Validate(scheduleSchema, json.RawMessage(`{}`)))
			handler := definition.Capabilities.InteractionHandler
			require.NotNil(t, handler)
			schema, err := json.Marshal(handler.InputSchema)
			require.NoError(t, err)
			require.NoError(t, jsonschema.Validate(schema, destination))
			require.Error(t, jsonschema.Validate(schema, json.RawMessage(`{}`)), "selection requires an explicit complete destination")
		})
	}
	require.Equal(t, 8, toolCount)
}
