package integrationdefinition

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/jsonschema"
	"github.com/stretchr/testify/require"
)

func TestDiscordDestinationSchemaAndValidationAgree(t *testing.T) {
	d, _ := Lookup(DiscordThread)
	schema, err := d.Subscription.ConversationSchema()
	require.NoError(t, err)
	for _, input := range []string{
		`{"thread_id":"789"}`,
		`{"guild_id":"123","thread_id":"789"}`,
		`{"channel_id":"456","thread_id":"789"}`,
		`{"channel_id":"456"}`,
	} {
		require.NoError(t, jsonschema.Validate(schema, []byte(input)), input)
		_, err := d.Subscription.Prepare([]byte(input))
		require.NoError(t, err, input)
	}
	for _, input := range []string{
		`{}`, `{"guild_id":"123"}`, `{"thread_id":""}`, `{"thread_id":"0"}`,
		`{"thread_id":"789","channel_id":""}`, `{"thread_id":"789","channel_id":"invalid"}`,
		`{"thread_id":"789","guild_id":"invalid"}`, `{"thread_id":"789","channel_id":null}`,
	} {
		require.Error(t, jsonschema.Validate(schema, []byte(input)), input)
		_, err := d.Subscription.Prepare([]byte(input))
		require.Error(t, err, input)
	}
	for _, address := range []DiscordScope{
		{}, {GuildID: "123"}, {ThreadID: "0"},
		{ThreadID: "789", ChannelID: "invalid"}, {ThreadID: "789", GuildID: "invalid"},
	} {
		require.Error(t, (Scope{Discord: &address}).Validate(ProviderDiscord), address)
	}
}
