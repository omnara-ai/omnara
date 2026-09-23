package apps

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/apps/discord"
	"github.com/omnara-ai/omnara/internal/apps/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/appstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func providerProfileChoice(t *testing.T) appstore.AppProfileChoiceRecord {
	t.Helper()
	return appstore.AppProfileChoiceRecord{
		ID:        uuid.New(),
		ExpiresAt: time.Date(2026, time.September, 18, 17, 4, 0, 0, time.FixedZone("fixture", 2*60*60)),
		Options: []appstore.AppProfileChoiceOption{
			{Key: "support", ProfileID: uuid.New(), Name: "Support"},
			{Key: "review", ProfileID: uuid.New(), Name: "Reviewer"},
		},
	}
}

func TestAppProfileChoiceDiscordProviderCreatesThreadAndPostsNativeMenu(t *testing.T) {
	t.Parallel()
	f, provider := newDiscordInboxFixture(t)
	f.appSetup.ProviderConfig = json.RawMessage(`{"public_key":"` + strings.Repeat("a", 64) + `"}`)
	event, ok, err := NormalizeDiscordAppEvent(f.appSetup, discordInboxPayload(t, f.message), f.channels["300"])
	require.NoError(t, err)
	require.True(t, ok)
	choice := providerProfileChoice(t)
	choice.Event, err = json.Marshal(event)
	require.NoError(t, err)
	choice.Address = appstore.ConversationAddress{Kind: "thread", Ref: "300:500"}
	choiceID, err := publicid.Encode(publicid.KindAppProfileChoice, choice.ID)
	require.NoError(t, err)
	requests := make(chan map[string]any, 4)
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/v10/channels/300/messages/500/threads" {
			var body map[string]string
			if assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				assert.Equal(t, "Helper conversation", body["name"])
			}
			return false
		}
		if r.URL.Path != "/api/v10/channels/500/messages" && r.URL.Path != "/api/v10/channels/500/messages/600" {
			return false
		}
		var body map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return true
		}
		if r.URL.Path == "/api/v10/channels/500/messages" {
			assert.Equal(t, http.MethodPost, r.Method)
		} else {
			assert.Equal(t, http.MethodPatch, r.Method)
		}
		requests <- body
		assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
			"id": "600", "channel_id": "500", "author": map[string]string{"id": "22"}, "nonce": body["nonce"],
		}))
		return true
	}
	var checks atomic.Int32
	check := func(context.Context) error { checks.Add(1); return nil }
	channel, message, err := provider.PresentProfileChoice(t.Context(), f.appSetup, choice, check)
	require.NoError(t, err)
	require.Equal(t, "500", channel)
	require.Equal(t, "600", message)
	body := <-requests
	require.Equal(t, true, body["enforce_nonce"])
	require.NotEmpty(t, body["nonce"])
	require.Equal(t, map[string]any{"parse": []any{}}, body["allowed_mentions"])
	require.Contains(t, body["content"], "Choose a profile before continuing")
	require.Contains(t, body["content"], "This menu expires Sep 18 at 15:04 UTC.")
	var rows []discord.ActionRow
	raw, err := json.Marshal(body["components"])
	require.NoError(t, err)
	require.NoError(t, json.Unmarshal(raw, &rows))
	require.Len(t, rows, 1)
	require.Len(t, rows[0].Components, 1)
	selectMenu := rows[0].Components[0]
	require.Equal(t, 3, selectMenu.Type)
	require.Equal(t, discord.ProfileChoiceCustomIDPrefix+choiceID, selectMenu.CustomID)
	require.Equal(t, []discord.SelectOption{{Label: "Support", Value: "support"}, {Label: "Reviewer", Value: "review"}},
		selectMenu.Options)
	require.Equal(t, 1, selectMenu.MinValues)
	require.Equal(t, 1, selectMenu.MaxValues)
	require.Positive(t, checks.Load())

	channel, message, err = provider.PresentProfileChoice(t.Context(), f.appSetup, choice, check)
	require.NoError(t, err)
	require.Equal(t, "500", channel)
	require.Equal(t, "600", message)
	require.Equal(t, body, <-requests)
	f.mu.Lock()
	threadPosts := f.posts
	f.mu.Unlock()
	require.Equal(t, 1, threadPosts)
	choice.MessageChannelID, choice.MessageID = channel, message
	require.NoError(t, provider.DismissProfileChoice(t.Context(), f.appSetup, choice, "Selected Reviewer."))
	dismissal := <-requests
	require.Equal(t, "Selected Reviewer.", dismissal["content"])
	require.Equal(t, []any{}, dismissal["components"])

	f.mu.Lock()
	f.revoked = true
	f.mu.Unlock()
	_, _, err = provider.PresentProfileChoice(t.Context(), f.appSetup, choice, check)
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	require.Empty(t, requests)
}

func TestAppProfileChoiceSlackProviderPreservesRetryHint(t *testing.T) {
	t.Parallel()
	for _, operation := range []string{"post", "update"} {
		t.Run(operation, func(t *testing.T) {
			t.Parallel()
			appSetup := slackInboxTestApp()
			access := &slackInboxTestAccess{appSetup: appSetup, version: uuid.New()}
			var attempts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth.test" {
					_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
					return
				}
				path := "/chat.postMessage"
				if operation == "update" {
					path = "/chat.update"
				}
				assert.Equal(t, path, r.URL.Path)
				attempts.Add(1)
				w.Header().Set("Retry-After", "900")
				w.WriteHeader(http.StatusTooManyRequests)
				_, _ = w.Write([]byte(`{"ok":false,"error":"ratelimited"}`))
			}))
			t.Cleanup(server.Close)
			provider := NewSlackAppInboxProvider(slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				access, access, nil)
			choice := providerProfileChoice(t)
			choice.Address = appstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
			var err error
			if operation == "post" {
				var channel, message string
				channel, message, err = provider.PresentProfileChoice(t.Context(), appSetup, choice, nil)
				require.Empty(t, channel)
				require.Empty(t, message)
			} else {
				choice.MessageChannelID, choice.MessageID = "C123", "2.3"
				err = provider.DismissProfileChoice(t.Context(), appSetup, choice, "Selected Reviewer.")
			}
			var apiErr *slack.APIError
			require.ErrorAs(t, err, &apiErr)
			require.True(t, apiErr.Result.RateLimited)
			require.Equal(t, 15*time.Minute, apiErr.RetryDelay())
			require.Equal(t, 15*time.Minute, appInboxRetryDelay(1, err))
			require.EqualValues(t, 1, attempts.Load(), "long provider throttles must return to the durable worker")
		})
	}
}

func TestAppProfileChoiceSlackProviderPostsAndClearsNativeMenu(t *testing.T) {
	t.Parallel()
	appSetup := slackInboxTestApp()
	access := &slackInboxTestAccess{appSetup: appSetup, version: uuid.New()}
	requests := make(chan map[string]any, 3)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth.test" {
			_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
			return
		}
		assert.Equal(t, "Bearer test-token", r.Header.Get("Authorization"))
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Contains(t, []string{"/chat.postMessage", "/chat.update"}, r.URL.Path)
		var body map[string]any
		if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
			http.Error(w, "invalid body", http.StatusBadRequest)
			return
		}
		requests <- body
		_, _ = w.Write([]byte(`{"ok":true,"channel":"C123","ts":"2.3"}`))
	}))
	t.Cleanup(server.Close)
	provider := NewSlackAppInboxProvider(slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		access, access, nil)
	choice := providerProfileChoice(t)
	choice.Address = appstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
	choiceID, err := publicid.Encode(publicid.KindAppProfileChoice, choice.ID)
	require.NoError(t, err)
	var checks atomic.Int32
	check := func(context.Context) error { checks.Add(1); return nil }
	channel, message, err := provider.PresentProfileChoice(t.Context(), appSetup, choice, check)
	require.NoError(t, err)
	require.Equal(t, "C123", channel)
	require.Equal(t, "2.3", message)
	body := <-requests
	require.Equal(t, "C123", body["channel"])
	require.Equal(t, "1.2", body["thread_ts"])
	require.Contains(t, body["text"], "Choose a profile before continuing")
	require.Contains(t, body["text"], "This menu expires Sep 18 at 15:04 UTC.")
	raw, err := json.Marshal(body["blocks"])
	require.NoError(t, err)
	require.Contains(t, string(raw), slack.ProfileChoiceActionPrefix+choiceID)
	require.Contains(t, string(raw), `"type":"static_select"`)
	for _, option := range choice.Options {
		require.Contains(t, string(raw), `"value":"`+option.Key+`"`)
		require.NotContains(t, string(raw), option.ProfileID.String())
	}
	require.Positive(t, checks.Load())
	choice.MessageChannelID, choice.MessageID = channel, message
	require.NoError(t, provider.DismissProfileChoice(t.Context(), appSetup, choice, "Selected Reviewer."))
	dismissal := <-requests
	require.Equal(t, "C123", dismissal["channel"])
	require.Equal(t, "2.3", dismissal["ts"])
	require.Equal(t, "Selected Reviewer.", dismissal["text"])
	require.Equal(t, []any{}, dismissal["blocks"])
	_, _, err = provider.PresentProfileChoice(t.Context(), appSetup, choice,
		func(context.Context) error { return storeerr.ErrUnauthorized })
	require.ErrorIs(t, err, storeerr.ErrUnauthorized)
	require.Empty(t, requests, "revoked app authority must prevent provider I/O")
}
