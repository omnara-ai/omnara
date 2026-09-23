package executionstore

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/toolpermission"
	"github.com/stretchr/testify/require"
)

func TestAppInteractionOriginArguments(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, provider, kind, ref, args string
	}{
		{"Slack thread", "slack", "thread", "C123:111.222", `{"channel_id":"C123","thread_ts":"111.222"}`},
		{"Slack channel", "slack", "channel", "C123", `{"channel_id":"C123"}`},
		{"DM", "slack", "dm", "D123", `{"channel_id":"D123"}`},
		{"DM kind matters", "slack", "channel", "D123", ""},
		{"malformed thread", "slack", "thread", "C123:not-a-timestamp", ""},
		{"malformed address", "slack", "thread", "C123", ""},
		{"Discord thread", "discord", "thread", "20:30", `{"channel_id":"20","thread_id":"30"}`},
		{"Discord has no DM kind", "discord", "dm", "20", ""},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			args, ok := interactionArgsForOrigin(
				test.provider,
				appstore.ConversationAddress{Kind: test.kind, Ref: test.ref},
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
		AppType:     appdefinition.SlackThread,
		HandlerKey:  "chat",
		AppID:       uuid.New(),
		AppTargetID: uuid.New(),
		Args:        json.RawMessage(`{"channel_id":"C123","thread_ts":"111.222"}`),
		Address:     appstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"},
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
		func(d *InteractionDestination) { d.AppType = appdefinition.GitHubPR },
		func(d *InteractionDestination) { d.AppType = "slack_unregistered" },
		func(d *InteractionDestination) { d.AppType = "" },
		func(d *InteractionDestination) { d.Address.Ref = "C456:111.222" },
		func(d *InteractionDestination) {
			d.Args = json.RawMessage(`{"channel_id":"C456","thread_ts":"111.222"}`)
		},
		func(d *InteractionDestination) {
			d.Args = json.RawMessage(`{"thread_ts":"111.222"}`)
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
		AppType:     appdefinition.SlackThread,
		HandlerKey:  "chat",
		AppID:       uuid.New(),
		AppTargetID: uuid.New(),
		Args:        json.RawMessage(`{"channel_id":"C123","thread_ts":"111.222"}`),
		Address:     appstore.ConversationAddress{Kind: "thread", Ref: "C123:111.222"},
	}
	equal := original
	equal.Args = json.RawMessage(`{ "channel_id" : "C123", "thread_ts":"111.222" }`)
	require.True(t, sameInteractionDestination(original, equal))
	for _, change := range []func(*InteractionDestination){
		func(d *InteractionDestination) { d.HandlerKey = "replacement" },
		func(d *InteractionDestination) { d.AppID = uuid.New() },
		func(d *InteractionDestination) { d.AppTargetID = uuid.New() },
		func(d *InteractionDestination) { d.AppType = appdefinition.DiscordThread },
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
