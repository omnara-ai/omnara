package config

import (
	"encoding/base64"
	"encoding/json"
	"strings"
	"testing"

	openapispec "github.com/omnara-ai/omnara/api/openapi"
	"github.com/omnara-ai/omnara/internal/events"
	"github.com/stretchr/testify/require"
)

func TestSpecSchemaMatchesEventTypes(t *testing.T) {
	raw, err := openapispec.ComponentJSONSchema("EventWebhookEventType")
	require.NoError(t, err)
	var schema struct {
		Enum []string `json:"enum"`
	}
	require.NoError(t, json.Unmarshal(raw, &schema))
	require.Equal(t, []string{
		string(events.KindAgentInput), string(events.KindModelOutput), string(events.KindToolResult),
		string(events.KindContextCheckpoint), "tool_call_update",
	}, schema.Enum)
	_, err = Validate(Settings{URL: "https://example.com/events", EventTypes: schema.Enum})
	require.NoError(t, err)
}

func TestValidate(t *testing.T) {
	settings, err := Validate(Settings{URL: " https://example.com/events?source=agent ",
		EventTypes: []string{"model_output", "tool_call_update"}})
	require.NoError(t, err)
	require.Equal(t, "https://example.com/events?source=agent", settings.URL)
	for _, eventTypes := range [][]string{nil, {}, {"unknown"}, {"model_output", "model_output"}} {
		_, err := Validate(Settings{URL: settings.URL, EventTypes: eventTypes})
		require.Error(t, err)
	}
}

func TestRejectInvalidURLs(t *testing.T) {
	for _, endpoint := range []string{
		"https://10.0.0.5/hook", "https://127.0.0.1/hook", "https://[::1]/hook",
		"https://[fd00::1]/hook", "https://169.254.169.254/hook", "https://localhost/hook",
		"https://LOCALHOST./hook", "https://[::ffff:127.0.0.1]/hook",
		"", "/events", "http://example.com/events", "ftp://example.com/events", "https:///events",
		"https://user:password@example.com/events", "https://example.com/events#fragment",
		"https://example.com:bad/events",
	} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := ValidateURL(endpoint)
			require.Error(t, err)
		})
	}
}

func TestAllowPublicHosts(t *testing.T) {
	for _, endpoint := range []string{"https://example.com/events",
		"https://8.8.8.8/events", "https://[2606:4700:4700::1111]/events"} {
		got, err := ValidateURL(endpoint)
		require.NoError(t, err)
		require.Equal(t, endpoint, got)
	}
}

func TestURLLength(t *testing.T) {
	prefix := "https://example.com/"
	endpoint := prefix + strings.Repeat("a", 2048-len(prefix))
	_, err := ValidateURL(endpoint)
	require.NoError(t, err)
	_, err = ValidateURL(endpoint + "a")
	require.ErrorContains(t, err, "2048 bytes")
	_, err = ValidateURL(prefix + strings.Repeat("é", 400))
	require.ErrorContains(t, err, "2048 bytes")
}

func TestDecodeSigningKey(t *testing.T) {
	for _, size := range []int{0, 23, 24, 32, 64, 65} {
		value := strings.Repeat("k", size)
		for _, prefix := range []string{"", "whsec_"} {
			key, err := DecodeSigningKey(prefix + base64.StdEncoding.EncodeToString([]byte(value)))
			if size < 24 || size > 64 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, []byte(value), key)
			}
		}
	}
	_, err := DecodeSigningKey("not-base64")
	require.Error(t, err)
}
