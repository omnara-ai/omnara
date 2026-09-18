package agentconfig

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/stretchr/testify/require"
)

func TestEventWebhookConfig(t *testing.T) {
	result, err := Compile(SourceFormatYAML, []byte(validAgentSource(`
event_webhook:
  url: " https://example.com/events?source=agent "
`)), CompileOptions{})
	require.NoError(t, err)
	require.Equal(t, &EventWebhook{URL: "https://example.com/events?source=agent"}, result.Compiled.EventWebhook)
	child, err := SubagentCompiledFrom(result.Compiled, SubagentCompiled{Type: SubagentTypeSelf}, SubagentDepth{}, nil)
	require.NoError(t, err)
	require.Equal(t, result.Compiled.EventWebhook, child.EventWebhook)
}

func TestEventWebhookEventFilters(t *testing.T) {
	for _, filter := range []string{"", "[tool_call_update, model_output]"} {
		t.Run(filter, func(t *testing.T) {
			source := "event_webhook:\n  url: https://example.com/events\n"
			if filter != "" {
				source += "  events: " + filter + "\n"
			}
			result, err := Compile(SourceFormatYAML, []byte(validAgentSource(source)), CompileOptions{})
			require.NoError(t, err)
			var compiled Compiled
			require.NoError(t, json.Unmarshal(result.CanonicalJSON, &compiled))
			switch filter {
			case "":
				require.Nil(t, compiled.EventWebhook.Events)
			default:
				require.Equal(t, []string{"tool_call_update", "model_output"}, compiled.EventWebhook.Events)
			}
		})
	}
	for _, filter := range []string{"[]", "[unknown]", "[model_output, model_output]", "null"} {
		_, err := Compile(SourceFormatYAML, []byte(validAgentSource(
			"event_webhook:\n  url: https://example.com/events\n  events: "+filter+"\n",
		)), CompileOptions{})
		require.Error(t, err)
	}
}

func TestEventWebhookRejectsInvalidURLs(t *testing.T) {
	for _, endpoint := range []string{
		"https://10.0.0.5/hook", "https://127.0.0.1/hook", "https://[::1]/hook",
		"https://[fd00::1]/hook", "https://169.254.169.254/hook", "https://localhost/hook",
		"https://LOCALHOST./hook", "https://[::ffff:127.0.0.1]/hook",
		"", "/events", "http://example.com/events", "ftp://example.com/events", "https:///events",
		"https://user:password@example.com/events", "https://example.com/events#fragment", "https://example.com:bad/events",
	} {
		t.Run(endpoint, func(t *testing.T) {
			_, err := ValidateEventWebhookURL(endpoint)
			require.Error(t, err)
		})
	}
	_, err := ParseSource(SourceFormatJSON, []byte(`{
 "instruction":"test", "model":{"provider_config":"test","name":"test"},
 "event_webhook":{"url":"https://example.com","headers":{}}
 }`))
	require.Error(t, err)
}

func TestEventWebhookValidatesSigningSecretReference(t *testing.T) {
	secretID, err := publicid.Encode(publicid.KindSecret, uuid.New())
	require.NoError(t, err)
	source := []byte(validAgentSource(
		"event_webhook:\n  url: https://example.com/events\n  signing_secret_id: " + secretID + "\n",
	))
	called := false
	opts := CompileOptions{ValidateSecretID: func(id string, kind secrets.Kind) error {
		called = true
		require.Equal(t, secretID, id)
		require.Equal(t, secrets.KindGeneric, kind)
		return nil
	}}
	result, err := Compile(SourceFormatYAML, source, opts)
	require.NoError(t, err)
	require.True(t, called)
	require.Equal(t, secretID, result.Compiled.EventWebhook.SigningSecretID)
	opts.ValidateSecretID = func(string, secrets.Kind) error { return errors.New("secret unavailable") }
	_, err = Compile(SourceFormatYAML, source, opts)
	require.ErrorContains(t, err, "secret unavailable")
	invalid := []byte(validAgentSource(
		"event_webhook:\n  url: https://example.com/events\n  signing_secret_id: invalid\n",
	))
	_, err = Compile(SourceFormatYAML, invalid, CompileOptions{})
	require.Error(t, err)
}

func TestEventWebhookAllowsPublicHosts(t *testing.T) {
	for _, endpoint := range []string{
		"https://example.com/events", "https://8.8.8.8/events", "https://[2606:4700:4700::1111]/events",
	} {
		got, err := ValidateEventWebhookURL(endpoint)
		require.NoError(t, err)
		require.Equal(t, endpoint, got)
	}
}

func TestDecodeEventWebhookSigningKey(t *testing.T) {
	for _, size := range []int{0, 23, 24, 32, 64, 65} {
		value := strings.Repeat("k", size)
		for _, prefix := range []string{"", "whsec_"} {
			key, err := DecodeEventWebhookSigningKey(prefix + base64.StdEncoding.EncodeToString([]byte(value)))
			if size < 24 || size > 64 {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
				require.Equal(t, []byte(value), key)
			}
		}
	}
	_, err := DecodeEventWebhookSigningKey("not-base64")
	require.Error(t, err)
}
