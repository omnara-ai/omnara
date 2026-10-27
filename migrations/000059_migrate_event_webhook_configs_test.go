package migrations

import (
	"database/sql"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRemoveEventWebhookSource(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
	}{
		{"middle", "# note\ninstruction: hello\nevent_webhook:\n  url: https://example.com\n" +
			"  events: [model_output]\n# model note\nmodel: {name: test}\n",
			"# note\ninstruction: hello\n# model note\nmodel: {name: test}\n"},
		{"last", "instruction: >\n  intro\n\n   - indented\n\nevent_webhook: {url: https://example.com}\n...\n",
			"instruction: >\n  intro\n\n   - indented\n\n...\n"},
		{"first", "event_webhook: {}\ninstruction: event_webhook is literal text\n",
			"instruction: event_webhook is literal text\n"},
		{"quoted", "instruction: hello\n'event_webhook': {}\nmodel: {}\n", "instruction: hello\nmodel: {}\n"},
		{"unchanged", "instruction: event_webhook\n", "instruction: event_webhook\n"},
		{"crlf", "instruction: hi\r\nevent_webhook: {}\r\nmodel: {}\r\n", "instruction: hi\r\nmodel: {}\r\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := removeEventWebhookYAML([]byte(test.source))
			require.NoError(t, err)
			require.Equal(t, test.want, string(got))
		})
	}
	for _, source := range []string{
		`{instruction: hello, event_webhook: {url: https://example.com}, model: {}}`,
		`{"instruction":"hello","event_webhook":{"url":"https://example.com"},"model":{}}`,
	} {
		got, err := removeEventWebhookYAML([]byte(source))
		require.NoError(t, err)
		value, err := decodeAgentConfigNameMigrationYAML(got)
		require.NoError(t, err)
		require.Equal(t, map[string]any{"instruction": "hello", "model": map[string]any{}}, value)
	}
	_, err := removeEventWebhookYAML([]byte("event_webhook: {}\ninstruction: *missing\n"))
	require.Error(t, err)
	got, err := removeEventWebhookJSON([]byte(`{"event_webhook":{},"instruction":"<tag>&"}`), false)
	require.NoError(t, err)
	require.Equal(t, `{"instruction":"<tag>&"}`, string(got))
}

func TestRemoveEventWebhookYAMLReferences(t *testing.T) {
	for _, test := range []struct{ name, source string }{
		{"merged", "<<: {event_webhook: {url: https://example.com, events: [model_output]}}\n" +
			"instruction: >\n  intro\n\n   - indented\n\nmodel: {provider_config: openai, name: test}\n"},
		{"shared anchor", "event_webhook:\n  url: &endpoint https://example.com\n  events: [model_output]\n" +
			"instruction: *endpoint\nmodel: {provider_config: openai, name: test}\n"},
		{"overridden merge", "<<: {event_webhook: {url: https://example.com/old, events: [model_output]}}\n" +
			"event_webhook: {url: https://example.com/new, events: [tool_result]}\n" +
			"instruction: hello\nmodel: {provider_config: openai, name: test}\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			before, err := decodeAgentConfigNameMigrationYAML([]byte(test.source))
			require.NoError(t, err)
			expected, ok := before.(map[string]any)
			require.True(t, ok)
			delete(expected, "event_webhook")
			got, err := removeEventWebhookYAML([]byte(test.source))
			require.NoError(t, err)
			after, err := decodeAgentConfigNameMigrationYAML(got)
			require.NoError(t, err)
			require.Equal(t, expected, after)
			again, err := removeEventWebhookYAML(got)
			require.NoError(t, err)
			require.Equal(t, got, again)
		})
	}
}

func TestRewriteEventWebhookConfig(t *testing.T) {
	for _, format := range []string{"json", "yaml", ""} {
		t.Run(format, func(t *testing.T) {
			compiled := []byte(`{"event_webhook":{"events":["model_output"],"url":"https://example.com"},"instruction":"hello"}`)
			hash, err := explicitDefaultToolsConfigHash(compiled)
			require.NoError(t, err)
			config := eventWebhookConfig{id: "unchanged", compiled: compiled, hash: hash}
			if format != "" {
				source := string(compiled)
				if format == "yaml" {
					source = "instruction: hello\nevent_webhook: {url: https://example.com, events: [model_output]}\n"
				}
				config.source = sql.NullString{String: source, Valid: true}
				config.format = sql.NullString{String: format, Valid: true}
				config.sourceHash = sql.NullString{String: hashBytes([]byte(source)), Valid: true}
			}
			updated, err := rewriteEventWebhookConfig(config)
			require.NoError(t, err)
			require.Equal(t, config.id, updated.id)
			require.JSONEq(t, `{"instruction":"hello"}`, string(updated.compiled))
			expectedHash, err := explicitDefaultToolsConfigHash(updated.compiled)
			require.NoError(t, err)
			require.Equal(t, expectedHash, updated.hash)
			require.Equal(t, config.source.Valid, updated.source.Valid)
			if updated.source.Valid {
				require.NotContains(t, updated.source.String, "event_webhook")
				require.Equal(t, hashBytes([]byte(updated.source.String)), updated.sourceHash.String)
			}
			again, err := rewriteEventWebhookConfig(updated)
			require.NoError(t, err)
			require.Equal(t, updated, again)
			config.hash = "bad"
			_, err = rewriteEventWebhookConfig(config)
			require.ErrorContains(t, err, "hashes do not match")
		})
	}
}
