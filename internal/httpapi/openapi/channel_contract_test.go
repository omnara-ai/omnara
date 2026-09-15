package openapi

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestChannelSendAndObservationSchemas(t *testing.T) {
	t.Parallel()
	spec, err := GetSpec()
	require.NoError(t, err)
	channel := "itgt_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	artifact := "art_aaaaaaaaaaaaaaaaaaaaaaaaaa"
	destination := map[string]any{
		"implementation_key": "slack_channel", "provider_ref": "C1",
		"provider_ref_kind": "channel", "provider_metadata": map[string]any{},
	}
	for _, test := range []struct {
		name    string
		schema  string
		value   map[string]any
		invalid bool
	}{
		{"empty send content", "ChannelSendMessage", map[string]any{}, true},
		{"text send", "ChannelSendMessage", map[string]any{"text": "Hello"}, false},
		{"files-only send", "ChannelSendMessage", map[string]any{"artifact_ids": []any{artifact}}, false},
		{"null attachments", "ChannelSendMessage", map[string]any{"artifact_ids": nil}, true},
		{"empty attachments", "ChannelSendMessage", map[string]any{"artifact_ids": []any{}}, true},
		{"public empty send", "SendChannelMessageRequest", map[string]any{
			"channel_id": channel, "message": map[string]any{},
		}, true},
		{"gateway empty send", "ChannelSendOperation", map[string]any{
			"destination": destination, "message": map[string]any{}, "params": map[string]any{},
		}, true},
		{"empty observation", "ChannelMessageObservation", map[string]any{
			"content": map[string]any{}, "publication": "published",
		}, false},
		{"null observation", "ChannelMessageObservation", map[string]any{"content": nil, "publication": "published"}, true},
		{"known publication with failed continuation", "SendChannelMessageResult", map[string]any{
			"request_id":         "operation-1",
			"message":            map[string]any{"content": map[string]any{"text": "Posted"}, "publication": "published", "message_id": "provider-1"},
			"continuation_error": map[string]any{"code": "unavailable_channel", "message": "The message was posted, but its reply channel is unavailable."},
		}, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			schema := spec.Components.Schemas[test.schema]
			require.NotNil(t, schema)
			err := schema.Value.VisitJSON(test.value)
			if test.invalid {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
		})
	}
}
