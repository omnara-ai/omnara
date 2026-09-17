package httpapi

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestPublicCompiledDefinition(t *testing.T) {
	t.Parallel()
	id := uuid.MustParse("11111111-1111-4111-8111-111111111111")
	modelID, err := publicid.Encode(publicid.KindConfiguredModel, id)
	require.NoError(t, err)
	source := `{"model":{"configured_model_id":"11111111-1111-4111-8111-111111111111","future":9007199254740993},"instruction":"11111111-1111-4111-8111-111111111111","tools":{"custom":{"input_schema":{"const":18446744073709551615}},"skill":{"enabled":false}},"future":{"number":1.234567890123456789}}`
	raw := json.RawMessage(source)
	projected, err := publicCompiledDefinition(raw)
	require.NoError(t, err)
	require.Equal(t, source, string(raw))
	var stored, response map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &stored))
	require.NoError(t, json.Unmarshal(projected, &response))
	var storedModel, responseModel map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(stored["model"], &storedModel))
	require.NoError(t, json.Unmarshal(response["model"], &responseModel))
	var responseID string
	require.NoError(t, json.Unmarshal(responseModel["configured_model_id"], &responseID))
	require.Equal(t, modelID, responseID)
	delete(storedModel, "configured_model_id")
	delete(responseModel, "configured_model_id")
	require.Equal(t, storedModel, responseModel)
	delete(stored, "model")
	delete(response, "model")
	require.Equal(t, stored, response)
}

func TestPublicCompiledDefinitionInvalid(t *testing.T) {
	t.Parallel()
	for _, source := range []string{`{`, `null`, `{}`, `{"model":null}`, `{"model":[]}`} {
		t.Run(source, func(t *testing.T) {
			t.Parallel()
			_, err := publicCompiledDefinition(json.RawMessage(source))
			require.Error(t, err)
		})
	}
}

func TestPublicCompiledDefinitionResourceIDs(t *testing.T) {
	id := uuid.New()
	public := func(kind publicid.Kind) string {
		value, err := publicid.Encode(kind, id)
		require.NoError(t, err)
		return value
	}
	raw := json.RawMessage(`{"model":{"configured_model_id":"` + id.String() + `"},` +
		`"machine_sources":[{"machine_id":"` + id.String() + `","secret_env_overlay":{"TOKEN":"` + id.String() + `","REMOVE":null}},{"machine_pool_id":"` + id.String() + `"}],` +
		`"skills":[{"id":"` + id.String() + `"}],"subagents":{"self":{"type":"self"},"profile":{"profile_id":"` + id.String() + `"}},` +
		`"mcp":{"docs":{"auth":{"secret_id":"` + id.String() + `"}},"public":{"url":"https://example.com/mcp"}}}`)
	projected, err := publicCompiledDefinition(raw)
	require.NoError(t, err)
	require.JSONEq(t, `{"model":{"configured_model_id":"`+public(publicid.KindConfiguredModel)+`"},`+
		`"machine_sources":[{"machine_id":"`+public(publicid.KindMachine)+`","secret_env_overlay":{"TOKEN":"`+public(publicid.KindSecret)+`","REMOVE":null}},{"machine_pool_id":"`+public(publicid.KindMachinePool)+`"}],`+
		`"skills":[{"public_id":"`+public(publicid.KindSkill)+`"}],"subagents":{"self":{"type":"self"},"profile":{"profile_id":"`+public(publicid.KindAgentProfile)+`"}},`+
		`"mcp":{"docs":{"auth":{"secret_id":"`+public(publicid.KindSecret)+`"}},"public":{"url":"https://example.com/mcp"}}}`, string(projected))
	require.NotContains(t, string(projected), id.String())
}
