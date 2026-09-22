package integration

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func feedbackReceipt(app integrationstore.ProjectAppRecord, payload []byte) integrationstore.IntegrationInboxRecord {
	return integrationstore.IntegrationInboxRecord{
		ID: uuid.New(), ProjectID: app.ProjectID, AppID: app.ID, State: integrationstore.IntegrationInboxFailed,
		Source: integrationstore.IntegrationInboxSourceProvider, Payload: payload, LastError: "private failure details",
	}
}

func feedbackPlan(t *testing.T, scope appdefinition.Scope) json.RawMessage {
	t.Helper()
	return githubEventJSON(t, AppInboxPlan{"scheduled": {
		Scope: scope, AgentID: uuid.New(), Selection: &integrationstore.InboxAppSelection{},
		Launch: &executionstore.LaunchAgentInput{},
	}})
}

func TestSlackNotifyInboxFailureDestination(t *testing.T) {
	for _, tc := range []struct {
		name, channel, thread string
		selected, scheduled   bool
	}{
		{"root mention", "C123", "", false, false},
		{"planned reply", "C123", "1.1", false, false},
		{"DM", "D123", "", false, false},
		{"selected profile", "C123", "1.1", true, false},
		{"scheduled opening", "C123", "1.1", false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			app := slackInboxTestApp()
			access := &slackInboxTestAccess{appSetup: app, version: uuid.New()}
			event := slack.Event{Type: "message", User: "U123", Channel: tc.channel, TS: "1.2", ThreadTS: tc.thread}
			if tc.channel == "C123" && tc.thread == "" {
				event.Type, event.Text = "app_mention", "<@UBOT> help"
			}
			receipt := feedbackReceipt(app, slackInboxTestPayload(t, event))
			wantThread, wantText := tc.thread, inboxFailureMessage
			if wantThread == "" && tc.channel != "D123" {
				wantThread = event.TS
			}
			if tc.selected {
				normalized, ok, err := NormalizeSlackAppEvent(app, receipt.Payload)
				require.NoError(t, err)
				require.True(t, ok)
				receipt.Events = githubEventJSON(t, []AppEvent{normalized})
				receipt.Payload = []byte(`{"menu_callback":"not the source"}`)
				wantText = selectedInboxFailureMessage
			} else if tc.thread != "" && !tc.scheduled {
				receipt.Plan = feedbackPlan(t, appdefinition.Scope{Slack: &appdefinition.SlackScope{
					ChannelID: tc.channel, ThreadTS: tc.thread,
				}})
			}
			if tc.scheduled {
				receipt.Source = integrationstore.IntegrationInboxSourceScheduled
				receipt.Payload = []byte(`{"channel_id":"COTHER"}`)
				receipt.Plan = feedbackPlan(t, appdefinition.Scope{Slack: &appdefinition.SlackScope{
					ChannelID: tc.channel, ThreadTS: tc.thread,
				}})
				wantText = scheduledInboxFailureMessage
			}
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
				switch r.URL.Path {
				case "/auth.test":
					_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
				case "/chat.postMessage":
					posts.Add(1)
					var body map[string]string
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
					assert.Equal(t, tc.channel, body["channel"])
					assert.Equal(t, wantThread, body["thread_ts"])
					assert.Equal(t, wantText, body["text"])
					_, _ = w.Write([]byte(`{"ok":true,"ts":"9.1"}`))
				default:
					t.Errorf("unexpected feedback request: %s", r.URL.Path)
				}
			}))
			t.Cleanup(server.Close)
			p := NewSlackAppInboxProvider(slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				access, access, nil)
			require.NoError(t, p.NotifyInboxFailure(t.Context(), app, receipt, inboxFailureMessage))
			require.EqualValues(t, 1, posts.Load())
		})
	}
}

func TestSlackNotifyInboxFailureRechecksAuthority(t *testing.T) {
	for _, change := range []string{"revision", "credential version", "revoked"} {
		t.Run(change, func(t *testing.T) {
			app := slackInboxTestApp()
			access := &slackInboxTestAccess{appSetup: app, version: uuid.New()}
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/auth.test" {
					posts.Add(1)
					t.Error("revoked feedback reached provider")
					return
				}
				access.mu.Lock()
				switch change {
				case "revision":
					access.appSetup.SetupRevision++
				case "credential version":
					access.version = uuid.New()
				default:
					access.revoked = true
				}
				access.mu.Unlock()
				_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
			}))
			t.Cleanup(server.Close)
			p := NewSlackAppInboxProvider(slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				access, access, nil)
			receipt := feedbackReceipt(app, slackInboxTestPayload(t, slack.Event{
				Type: "app_mention", User: "U123", Channel: "C123", TS: "1.2",
			}))
			require.ErrorIs(t, p.NotifyInboxFailure(t.Context(), app, receipt, inboxFailureMessage), storeerr.ErrUnauthorized)
			require.Zero(t, posts.Load())
		})
	}
}

func TestSlackNotifyInboxFailureMentionSiblings(t *testing.T) {
	for _, thread := range []string{"", "1.1"} {
		for _, planned := range []bool{false, true} {
			app := slackInboxTestApp()
			access := &slackInboxTestAccess{appSetup: app, version: uuid.New()}
			var posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth.test":
					_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
				case "/chat.postMessage":
					posts.Add(1)
					_, _ = w.Write([]byte(`{"ok":true,"ts":"9.1"}`))
				default:
					t.Errorf("unexpected feedback request: %s", r.URL.Path)
				}
			}))
			t.Cleanup(server.Close)
			p := NewSlackAppInboxProvider(
				slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}, access, access, nil)
			for _, kind := range []string{"message", "app_mention"} {
				receipt := feedbackReceipt(app, slackInboxTestPayload(t, slack.Event{
					Type: kind, User: "U123", Channel: "C123", TS: "1.2", ThreadTS: thread, Text: "<@UBOT> help",
				}))
				if planned {
					receipt.Plan = feedbackPlan(t, appdefinition.Scope{
						Slack: &appdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.1"},
					})
				}
				require.NoError(t, p.NotifyInboxFailure(t.Context(), app, receipt, inboxFailureMessage))
			}
			require.EqualValues(t, 1, posts.Load(), "thread=%q planned=%t", thread, planned)
		}
	}
}

func TestDiscordNotifyInboxFailureDestination(t *testing.T) {
	for _, tc := range []struct {
		name                       string
		reply, selected, scheduled bool
		threadExists               bool
		wantChannel                string
	}{
		{"root before thread creation", false, false, false, false, "300"},
		{"root with thread", false, false, false, true, "500"},
		{"planned thread reply", true, false, false, false, "400"},
		{"selected root", false, true, false, false, "300"},
		{"selected reply", true, true, false, false, "400"},
		{"scheduled before thread creation", false, false, true, false, "300"},
		{"scheduled with thread", false, false, true, true, "500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f, p := newDiscordInboxFixture(t)
			if tc.reply {
				f.message.ChannelID, f.message.ID = "400", "501"
				f.message.Content, f.message.Mentions = "ordinary reply", nil
			}
			if tc.threadExists {
				f.channels["500"] = discord.Channel{ID: "500", GuildID: "100", ParentID: "300", Type: 11}
			}
			receipt := feedbackReceipt(f.appSetup, discordInboxPayload(t, f.message))
			wantText := inboxFailureMessage
			if tc.selected {
				event, ok, err := NormalizeDiscordAppEvent(f.appSetup, receipt.Payload, f.channels[f.message.ChannelID])
				require.NoError(t, err)
				require.True(t, ok)
				receipt.Events = githubEventJSON(t, []AppEvent{event})
				receipt.Payload = []byte(`{"interaction":"not the source"}`)
				wantText = selectedInboxFailureMessage
			} else if tc.reply {
				receipt.Plan = feedbackPlan(t, appdefinition.Scope{Discord: &appdefinition.DiscordScope{
					GuildID: "100", ChannelID: "300", ThreadID: "400",
				}})
			}
			if tc.scheduled {
				receipt.Source = integrationstore.IntegrationInboxSourceScheduled
				receipt.Payload = []byte(`{"channel_id":"999"}`)
				receipt.Plan = feedbackPlan(t, appdefinition.Scope{Discord: &appdefinition.DiscordScope{
					GuildID: "100", ChannelID: "300", ThreadID: "500",
				}})
				f.message.Author = discord.User{ID: "22", Bot: true}
				wantText = scheduledInboxFailureMessage
			}
			var sent int
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/messages") {
					return false
				}
				sent++
				assert.Equal(t, "/api/v10/channels/"+tc.wantChannel+"/messages", r.URL.Path)
				var body struct {
					Content, Nonce  string
					AllowedMentions struct{ Parse []string } `json:"allowed_mentions"`
				}
				assert.NoError(t, json.NewDecoder(r.Body).Decode(&body))
				assert.Equal(t, wantText, body.Content)
				assert.True(t, strings.HasPrefix(body.Nonce, "f_"))
				assert.Empty(t, body.AllowedMentions.Parse)
				nonce, _ := json.Marshal(body.Nonce)
				assert.NoError(t, json.NewEncoder(w).Encode(discord.Message{
					ID: "900", ChannelID: tc.wantChannel, Author: discord.User{ID: "22", Bot: true}, Nonce: nonce,
				}))
				return true
			}
			require.NoError(t, p.NotifyInboxFailure(t.Context(), f.appSetup, receipt, inboxFailureMessage))
			f.mu.Lock()
			defer f.mu.Unlock()
			require.Equal(t, 1, sent)
			require.Zero(t, f.posts, "failure feedback must not create a thread")
		})
	}
}

func TestDiscordNotifyInboxFailureRechecksAuthority(t *testing.T) {
	for _, change := range []string{"revision", "credential version", "revoked", "wrong guild"} {
		t.Run(change, func(t *testing.T) {
			f, p := newDiscordInboxFixture(t)
			app := f.appSetup
			f.message.ChannelID = "400"
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/api/v10/channels/400" {
					return false
				}
				switch change {
				case "revision":
					f.appSetup.SetupRevision++
				case "credential version":
					f.version = uuid.New()
				case "revoked":
					f.revoked = true
				default:
					channel := f.channels["400"]
					channel.GuildID = "999"
					assert.NoError(t, json.NewEncoder(w).Encode(channel))
					return true
				}
				return false
			}
			err := p.NotifyInboxFailure(
				t.Context(), app, feedbackReceipt(app, discordInboxPayload(t, f.message)), inboxFailureMessage)
			require.Error(t, err)
			f.mu.Lock()
			defer f.mu.Unlock()
			for _, request := range f.requests {
				require.False(t, strings.HasPrefix(request, "POST "), request)
			}
		})
	}
}

func TestInboxFailureSkipsUnplannedOrdinaryHumanMessages(t *testing.T) {
	slackApp, discordApp := slackInboxTestApp(), discordInboxApp()
	slackProvider, discordProvider := &SlackAppInboxProvider{}, &DiscordAppInboxProvider{}
	for _, thread := range []string{"", "1.1"} {
		receipt := feedbackReceipt(slackApp, slackInboxTestPayload(t, slack.Event{
			Type: "message", User: "U123", Channel: "C123", TS: "1.2", ThreadTS: thread, Text: "ordinary conversation",
		}))
		require.NoError(t, slackProvider.NotifyInboxFailure(t.Context(), slackApp, receipt, inboxFailureMessage))
	}
	for _, channel := range []string{"300", "400"} {
		message := discordInboxMessageFixture()
		message.ChannelID, message.Mentions = channel, nil
		receipt := feedbackReceipt(discordApp, discordInboxPayload(t, message))
		require.NoError(t, discordProvider.NotifyInboxFailure(t.Context(), discordApp, receipt, inboxFailureMessage))
	}
}

func TestInboxFailureSkipsUnknownScheduledOpeningsAndAutomatedMessages(t *testing.T) {
	slackApp, discordApp := slackInboxTestApp(), discordInboxApp()
	slackProvider, discordProvider := &SlackAppInboxProvider{}, &DiscordAppInboxProvider{}
	for _, plan := range []json.RawMessage{nil, json.RawMessage(`{}`)} {
		slackReceipt, discordReceipt := feedbackReceipt(slackApp, nil), feedbackReceipt(discordApp, nil)
		slackReceipt.Source, discordReceipt.Source = integrationstore.IntegrationInboxSourceScheduled,
			integrationstore.IntegrationInboxSourceScheduled
		slackReceipt.Plan, discordReceipt.Plan = plan, plan
		require.NoError(t, slackProvider.NotifyInboxFailure(t.Context(), slackApp, slackReceipt, inboxFailureMessage))
		require.NoError(t, discordProvider.NotifyInboxFailure(t.Context(), discordApp, discordReceipt, inboxFailureMessage))
	}
	unknownSlack, unknownDiscord := feedbackReceipt(slackApp, nil), feedbackReceipt(discordApp, nil)
	unknownSlack.Source, unknownDiscord.Source = integrationstore.IntegrationInboxSourceScheduled,
		integrationstore.IntegrationInboxSourceScheduled
	unknownSlack.Plan = feedbackPlan(t, appdefinition.Scope{Slack: &appdefinition.SlackScope{ChannelID: "C123"}})
	unknownDiscord.Plan = feedbackPlan(t, appdefinition.Scope{Discord: &appdefinition.DiscordScope{
		GuildID: "100", ChannelID: "300",
	}})
	require.NoError(t, slackProvider.NotifyInboxFailure(t.Context(), slackApp, unknownSlack, inboxFailureMessage))
	require.NoError(t, discordProvider.NotifyInboxFailure(t.Context(), discordApp, unknownDiscord, inboxFailureMessage))
	slackReceipt := feedbackReceipt(slackApp, slackInboxTestPayload(t, slack.Event{
		Type: "message", User: "UBOT", BotID: "B123", Channel: "C123", TS: "1.2",
	}))
	require.NoError(t, slackProvider.NotifyInboxFailure(t.Context(), slackApp, slackReceipt, inboxFailureMessage))
	message := discordInboxMessageFixture()
	message.Author.Bot = true
	require.NoError(t, discordProvider.NotifyInboxFailure(t.Context(), discordApp,
		feedbackReceipt(discordApp, discordInboxPayload(t, message)), inboxFailureMessage))
	slackReceipt.AppID = uuid.New()
	require.ErrorIs(t,
		slackProvider.NotifyInboxFailure(t.Context(), slackApp, slackReceipt, inboxFailureMessage), storeerr.ErrUnauthorized)
}
