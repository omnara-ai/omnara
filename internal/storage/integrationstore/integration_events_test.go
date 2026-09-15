package integrationstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntegrationEventPayloadValidation(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]string{
		"missing": "", "null": "null", "array": "[]", "scalar": `"message"`,
		"trailing": `{} {}`, "duplicate": `{"id":1,"id":2}`,
		"escaped duplicate": `{"id":1,"\u0069d":2}`,
		"nested duplicate":  `{"event":{"id":1,"id":2}}`,
		"nul":               `{"text":"\u0000"}`,
		"oversized":         `{"message":"` + strings.Repeat("a", MaxIntegrationEventPayloadBytes) + `"}`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			_, err := normalizeIntegrationEventPayload(json.RawMessage(value))
			require.Error(t, err)
		})
	}
	payload, err := normalizeIntegrationEventPayload(json.RawMessage(`{"event":{"sequence":9007199254740993}}`))
	require.NoError(t, err)
	require.Equal(t, `{"event":{"sequence":9007199254740993}}`, string(payload))
}
