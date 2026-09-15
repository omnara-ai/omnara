package integrationstore

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestChannelDefinitionRejectsInvalidSchemasBeforePersistence(t *testing.T) {
	for _, schema := range []string{
		`null`, `[]`,
		`{"type":"object","type":"string"}`,
		`{"type":"object","properties":{"x":{"$ref":"https://example.com/schema.json"}}}`,
		`{"type":"invalid"}`, `{"type":"object"} {}`,
	} {
		t.Run(schema, func(t *testing.T) {
			input := validChannelDefinitionForTest()
			input.SendParamsSchema = json.RawMessage(schema)
			_, err := normalizeChannelDefinition(input)
			require.Error(t, err)
		})
	}
	_, err := normalizeChannelDefinition(validChannelDefinitionForTest())
	require.NoError(t, err)
}

func validChannelDefinitionForTest() PublishChannelDefinitionInput {
	return PublishChannelDefinitionInput{
		ProjectID: uuid.New(), IntegrationInstallID: uuid.New(),
		ImplementationKey: "custom", Kind: ChannelKindExternal,
		SendParamsSchema: json.RawMessage(`{}`),
		Capabilities:     ChannelCapabilities{Send: true, Text: true},
	}
}
