package executionstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestAppInteractionOriginArguments(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, provider, config, kind, ref, args string
	}{
		{"flexible Slack", "slack", `{}`, "thread", "C123:111.222", `{"channel_id":"C123","thread_ts":"111.222"}`},
		{"fixed channel", "slack", `{"channel_id":"C123"}`, "thread", "C123:111.222", `{"thread_ts":"111.222"}`},
		{"fixed thread", "slack", `{"channel_id":"C123","thread_ts":"111.222"}`, "thread", "C123:111.222", `{}`},
		{"other channel", "slack", `{"channel_id":"C456"}`, "thread", "C123:111.222", ""},
		{"other thread", "slack", `{"channel_id":"C123","thread_ts":"999.000"}`, "thread", "C123:111.222", ""},
		{"fixed thread rejects parent", "slack", `{"channel_id":"C123","thread_ts":"111.222"}`, "channel", "C123", ""},
		{"DM", "slack", `{}`, "dm", "D123", `{"channel_id":"D123"}`},
		{"DM kind matters", "slack", `{}`, "channel", "D123", ""},
		{"prefix is not identity", "slack", `{"channel_id":"C123"}`, "thread", "C1234:111.222", ""},
		{"malformed thread", "slack", `{}`, "thread", "C123:not-a-timestamp", ""},
		{"malformed address", "slack", `{}`, "thread", "C123", ""},
		{"flexible Discord", "discord", `{}`, "thread", "20:30", `{"channel_id":"20","thread_id":"30"}`},
		{"fixed guild channel", "discord", `{"guild_id":"10","channel_id":"20"}`, "thread", "20:30", `{"thread_id":"30"}`},
		{"other Discord thread", "discord", `{"channel_id":"20","thread_id":"40"}`, "thread", "20:30", ""},
		{"Discord has no DM kind", "discord", `{}`, "dm", "20", ""},
		{"unsupported provider", "github", `{}`, "pull_request", "123#4", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			handler := appdefinition.InteractionHandlerDefinition{Provider: test.provider}
			args, ok := interactionArgsForOrigin(
				handler,
				json.RawMessage(test.config),
				integrationstore.ConversationAddress{Kind: test.kind, Ref: test.ref},
			)
			require.Equal(t, test.args != "", ok)
			if ok {
				require.JSONEq(t, test.args, string(args))
			}
		})
	}
}

func TestAppInteractionSnapshotAndReceiptBounds(t *testing.T) {
	t.Parallel()
	destination := InteractionDestination{
		HandlerDefinition:   appdefinition.Slack,
		HandlerKey:          "chat",
		AppID:               uuid.New(),
		IntegrationTargetID: uuid.New(),
		Config: json.RawMessage(
			`{"channel_id":"C123"}`,
		),
		Args:    json.RawMessage(`{"thread_ts":"111.222"}`),
		Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"},
	}
	raw, err := json.Marshal(destination)
	require.NoError(t, err)
	parsed, err := (AgentInteractionRecord{Destination: raw}).CapturedDestination()
	require.NoError(t, err)
	require.Equal(t, destination, *parsed)
	parsed, err = (AgentInteractionRecord{}).CapturedDestination()
	require.NoError(t, err)
	require.Nil(t, parsed)
	for _, raw := range []string{
		`null`, `[]`, `{}`, `{} {}`, `{"unexpected":true}`,
		strings.Repeat(" ", InteractionDestinationMaxBytes) + `{}`,
	} {
		_, err := (AgentInteractionRecord{Destination: json.RawMessage(raw)}).CapturedDestination()
		require.Error(t, err)
	}
	for _, change := range []func(*InteractionDestination){
		func(d *InteractionDestination) { d.AppID = uuid.Nil },
		func(d *InteractionDestination) { d.HandlerKey = "chat__alias" },
		func(d *InteractionDestination) { d.HandlerDefinition = appdefinition.GitHub },
		func(d *InteractionDestination) { d.Address.Ref = "C456:111.222" },
		func(d *InteractionDestination) { d.Config = json.RawMessage(`{"channel_id":"C456"}`) },
		func(d *InteractionDestination) {
			d.Args = json.RawMessage(`{"channel_id":"C123","thread_ts":"111.222"}`)
		},
	} {
		changed := destination
		change(&changed)
		require.Error(t, changed.validate())
	}
	for _, raw := range []string{
		`null`, `[]`, `{} {}`, `{"id":"bad\u0000id"}`,
		`{"id":"` + strings.Repeat("x", InteractionReceiptMaxBytes) + `"}`,
	} {
		require.Error(
			t,
			validateInteractionObject(json.RawMessage(raw), InteractionReceiptMaxBytes),
		)
	}
	require.NoError(
		t,
		validateInteractionObject(
			json.RawMessage(`{"channel_id":"C123","message_ts":"111.222"}`),
			InteractionReceiptMaxBytes,
		),
	)
}

func TestAppInteractionSnapshotEqualityChecksAllAuthority(t *testing.T) {
	t.Parallel()
	original := InteractionDestination{
		HandlerDefinition:   appdefinition.Slack,
		HandlerKey:          "chat",
		AppID:               uuid.New(),
		IntegrationTargetID: uuid.New(),
		Config: json.RawMessage(
			`{"channel_id":"C123"}`,
		),
		Args:    json.RawMessage(`{"thread_ts":"111.222"}`),
		Address: integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"},
	}
	equal := original
	equal.Config = json.RawMessage(`{ "channel_id" : "C123" }`)
	require.True(t, sameInteractionDestination(original, equal))
	for _, change := range []func(*InteractionDestination){
		func(d *InteractionDestination) { d.HandlerKey = "replacement" },
		func(d *InteractionDestination) { d.AppID = uuid.New() },
		func(d *InteractionDestination) { d.IntegrationTargetID = uuid.New() },
		func(d *InteractionDestination) { d.HandlerDefinition = appdefinition.Discord },
		func(d *InteractionDestination) { d.Config = json.RawMessage(`{}`) },
		func(d *InteractionDestination) { d.Args = json.RawMessage(`{}`) },
		func(d *InteractionDestination) { d.Address.Ref = "C123:333.444" },
	} {
		changed := original
		change(&changed)
		require.False(t, sameInteractionDestination(original, changed))
	}
}

func TestAppInteractionPendingSelectionToolPermission(t *testing.T) {
	t.Parallel()
	original := agentconfig.RuntimeContract{
		Tools: []agentconfig.RuntimeTool{
			{
				Name:       toolcatalog.ToolNameSetInteractionHandler,
				Permission: toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
			},
		},
	}
	require.True(t, interactionSelectionToolAuthorized(original, original))
	require.False(t, interactionSelectionToolAuthorized(original, agentconfig.RuntimeContract{}))
	for _, mode := range []string{toolpermission.ModeAlwaysDeny, toolpermission.ModeAlwaysAllow} {
		current := agentconfig.RuntimeContract{
			Tools: []agentconfig.RuntimeTool{
				{
					Name:       toolcatalog.ToolNameSetInteractionHandler,
					Permission: toolpermission.DefaultSelection(mode),
				},
			},
		}
		require.False(t, interactionSelectionToolAuthorized(original, current))
	}
}
