package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscordLaunchUnavailableFeedbackUsesStableNonceAndCorrectReason(t *testing.T) {
	for _, scenario := range []string{"thread starter", "existing thread", "missing interaction setup"} {
		t.Run(scenario, func(t *testing.T) {
			f, provider := newDiscordInboxFixture(t)
			if scenario == "existing thread" {
				f.message.ChannelID = "400"
			}
			raw := discordInboxPayload(t, f.message)
			event, ok, err := normalizeDiscordIntegrationEvent(f.integrationSetup, raw, f.channels[f.message.ChannelID])
			require.NoError(t, err)
			require.True(t, ok)
			input := IntegrationLaunchContext{
				Integration: f.integrationSetup, Receipt: feedbackReceipt(f.integrationSetup, raw), Event: event,
			}
			cause := ErrIntegrationLaunchUnavailable
			if scenario == "missing interaction setup" {
				choice := providerProfileChoice(t)
				choice.Event, err = json.Marshal(event)
				require.NoError(t, err)
				_, _, cause = provider.PresentProfileChoice(t.Context(), f.integrationSetup, choice,
					func(context.Context) error { return nil })
				require.ErrorIs(t, cause, ErrIntegrationLaunchUnavailable)
				require.ErrorIs(t, cause, errDiscordProfileChoiceSetup)
				require.Empty(t, f.requests)
			}
			bodies := make(chan map[string]any, 3)
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if !strings.HasSuffix(r.URL.Path, "/messages") {
					return false
				}
				assert.Equal(t, "/api/v10/channels/"+f.message.ChannelID+"/messages", r.URL.Path)
				assert.Equal(t, http.MethodPost, r.Method)
				var body map[string]any
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					w.WriteHeader(http.StatusBadRequest)
					return true
				}
				bodies <- body
				assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
					"id": "900", "channel_id": f.message.ChannelID, "nonce": body["nonce"],
				}))
				return true
			}
			workflow := &IntegrationLaunchWorkflow{providers: map[string]IntegrationInboxProvider{"discord": provider}}
			workflow.launchUnavailable(t.Context(), input, cause)
			workflow.launchUnavailable(t.Context(), input, cause)
			input.Receipt.ID = uuid.New()
			workflow.launchUnavailable(t.Context(), input, cause)
			require.Len(t, bodies, 3, "feedback must reach the real Discord HTTP client")
			first, replay, next := <-bodies, <-bodies, <-bodies
			require.Equal(t, launchUnavailableMessage, first["content"])
			require.Equal(t, true, first["enforce_nonce"])
			require.Equal(t, map[string]any{"parse": []any{}}, first["allowed_mentions"])
			nonce, ok := first["nonce"].(string)
			require.True(t, ok)
			require.NotEmpty(t, nonce)
			require.LessOrEqual(t, len(nonce), 25)
			require.Equal(t, first, replay)
			require.NotEqual(t, first["nonce"], next["nonce"])
			f.mu.Lock()
			threads := f.posts
			f.mu.Unlock()
			require.Zero(t, threads, "unavailable launch feedback must not create a thread")
		})
	}
}

func TestSlackLaunchUnavailableFeedbackIncludesDMAndOnlyOneMentionSibling(t *testing.T) {
	for _, scenario := range []struct{ name, channel, thread string }{
		{"DM", "D123", ""}, {"root mention", "C123", ""}, {"thread mention", "C123", "1.1"},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			integration := slackInboxTestIntegration()
			access := &slackInboxTestAccess{integrationSetup: integration, version: uuid.New()}
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth.test":
					_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
				case "/chat.postMessage":
					posts.Add(1)
					var body map[string]string
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					assert.Equal(t, scenario.channel, body["channel"])
					thread := scenario.thread
					if scenario.channel == "C123" && thread == "" {
						thread = "1.2"
					}
					assert.Equal(t, thread, body["thread_ts"])
					assert.Equal(t, launchUnavailableMessage, body["text"])
					_, _ = w.Write([]byte(`{"ok":true,"ts":"9.1"}`))
				default:
					t.Errorf("unexpected provider request: %s", r.URL.Path)
				}
			}))
			t.Cleanup(server.Close)
			provider := NewSlackIntegrationInboxProvider(
				slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}, access, access, nil)
			workflow := &IntegrationLaunchWorkflow{providers: map[string]IntegrationInboxProvider{"slack": provider}}
			kinds := []string{"message", "app_mention"}
			if scenario.channel == "D123" {
				kinds = []string{"message"}
			}
			for _, kind := range kinds {
				raw := slackInboxTestPayload(t, slack.Event{
					Type: kind, User: "U123", Channel: scenario.channel, TS: "1.2",
					ThreadTS: scenario.thread, Text: "<@UBOT> help",
				})
				event, ok, err := NormalizeSlackIntegrationEvent(integration, raw)
				require.NoError(t, err)
				require.True(t, ok)
				workflow.launchUnavailable(t.Context(), IntegrationLaunchContext{
					Integration: integration, Receipt: feedbackReceipt(integration, raw), Event: event,
				}, ErrIntegrationLaunchUnavailable)
			}
			require.EqualValues(t, 1, posts.Load())
		})
	}
}
