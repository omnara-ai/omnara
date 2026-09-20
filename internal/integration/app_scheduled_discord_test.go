package integration

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDiscordScheduledRootAndThreadRecovery(t *testing.T) {
	f, provider := newDiscordInboxFixture(t)
	receipt := uuid.New()
	nonce := base64.RawURLEncoding.EncodeToString(receipt[:])
	require.Len(t, nonce, 22)
	// Historical GET responses do not carry the create response's nonce.
	f.message = discord.Message{ID: "500", ChannelID: "300", Author: discord.User{ID: "22", Bot: true}}
	posts := 0
	f.override = func(w http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path != "/api/v10/channels/300/messages" {
			return false
		}
		if r.Method == http.MethodPost {
			posts++
			var body map[string]json.RawMessage
			if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
				http.Error(w, "invalid message body", http.StatusBadRequest)
				return true
			}
			assert.JSONEq(t, `"`+nonce+`"`, string(body["nonce"]))
			assert.Equal(t, "true", string(body["enforce_nonce"]))
			_, _ = w.Write([]byte(`{"id":"500","channel_id":"300","author":{"id":"22","bot":true},"nonce":"` + nonce + `"}`))
		} else {
			t.Errorf("unexpected history scan: %s %s", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
		return true
	}
	now := time.Now()
	launch := integrationstore.ScheduledAppLaunch{Destination: json.RawMessage(`{"channel_id":"300"}`), OpeningMessage: "Daily update"}
	prep := integrationstore.ScheduledLaunchPreparation{AttemptedAt: &now}
	check := func(context.Context) error { return nil }
	root, notSent, err := provider.PublishScheduledRoot(t.Context(), f.appSetup, launch, receipt, prep, true, check)
	require.NoError(t, err)
	require.False(t, notSent)
	require.Equal(t, "500", root.Discord.ThreadID)
	require.Equal(t, "100", root.Discord.GuildID)
	require.Zero(t, f.posts, "root publication does not create a thread before the durable plan")
	prep.Root = &root
	// Resume thread preparation from the saved address without publishing again.
	require.NoError(t, provider.EnsureScheduledThread(t.Context(), f.appSetup, *prep.Root, check))
	require.NoError(t, provider.EnsureScheduledThread(t.Context(), f.appSetup, *prep.Root, check))
	require.Equal(t, 1, f.posts, "thread ensure reuses the message's one thread")
	require.Equal(t, 1, posts, "saved-root recovery never republishes the opening")
}

func TestDiscordScheduledUnknownPublicationDoesNotPostOnRetry(t *testing.T) {
	for _, test := range []struct {
		name      string
		status    int
		wantPosts int
	}{
		{name: "exhausted server errors", status: http.StatusInternalServerError, wantPosts: 3},
		{name: "missing message ID", status: http.StatusOK, wantPosts: 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			f, provider := newDiscordInboxFixture(t)
			receipt := uuid.New()
			nonce := base64.RawURLEncoding.EncodeToString(receipt[:])
			posts := 0
			f.override = func(w http.ResponseWriter, r *http.Request) bool {
				if r.URL.Path != "/api/v10/channels/300/messages" {
					return false
				}
				if !assert.Equal(t, http.MethodPost, r.Method, "uncertain sends must not scan history") {
					w.WriteHeader(http.StatusNotFound)
					return true
				}
				posts++
				var body map[string]json.RawMessage
				if !assert.NoError(t, json.NewDecoder(r.Body).Decode(&body)) {
					http.Error(w, "invalid message body", http.StatusBadRequest)
					return true
				}
				assert.JSONEq(t, `"`+nonce+`"`, string(body["nonce"]), "every initial attempt uses the same nonce")
				assert.Equal(t, "true", string(body["enforce_nonce"]))
				w.WriteHeader(test.status)
				if test.status == http.StatusOK {
					_, _ = w.Write([]byte(`{"channel_id":"300","author":{"id":"22","bot":true},"nonce":"` + nonce + `"}`))
				}
				return true
			}
			now := time.Now()
			prep := integrationstore.ScheduledLaunchPreparation{AttemptedAt: &now}
			launch := integrationstore.ScheduledAppLaunch{
				Destination: json.RawMessage(`{"channel_id":"300"}`), OpeningMessage: "Daily update",
			}
			check := func(context.Context) error { return nil }
			root, notSent, err := provider.PublishScheduledRoot(t.Context(), f.appSetup, launch, receipt, prep, true, check)
			require.ErrorIs(t, err, ErrScheduledLaunchFailed)
			require.ErrorContains(t, err, "outcome is unknown")
			require.False(t, notSent, "unknown delivery must preserve the publication attempt")
			require.Equal(t, appdefinition.Scope{}, root)
			require.Equal(t, test.wantPosts, posts)

			requests := len(f.requests)
			_, notSent, err = provider.PublishScheduledRoot(t.Context(), f.appSetup, launch, receipt, prep, false, check)
			require.ErrorIs(t, err, ErrScheduledLaunchFailed)
			require.ErrorContains(t, err, "outcome is unknown")
			require.False(t, notSent)
			require.Equal(t, test.wantPosts, posts, "a resumed worker never republishes an uncertain opening")
			require.Len(t, f.requests, requests, "resumed uncertainty fails before any provider request")
		})
	}
}
