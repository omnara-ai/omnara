package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func slackInboxTestConnection() integrationstore.IntegrationConnectionRecord {
	return integrationstore.IntegrationConnectionRecord{
		ID:                 uuid.New(),
		OrgID:              uuid.New(),
		ProjectID:          uuid.New(),
		CredentialSecretID: uuid.New(),
		State:              integrationstore.IntegrationConnectionStateActive,
		UpdatedAt:          time.Now(),
		Provider:           "slack",
		ProviderTenantID:   "T123",
		ProviderAccountRef: "A123",
		ProviderIdentity:   json.RawMessage(`{"bot_user_id":"UBOT"}`),
	}
}

func slackInboxTestPayload(t *testing.T, event slack.Event) []byte {
	t.Helper()
	raw, err := json.Marshal(event)
	require.NoError(t, err)
	payload, err := json.Marshal(
		slack.EventsEnvelope{
			Type:           "event_callback",
			TeamID:         "T123",
			APIAppID:       "A123",
			EventID:        "Ev123",
			Authorizations: []slack.Authorization{{TeamID: "T123", UserID: "UBOT", IsBot: true}},
			RawEvent:       raw,
		},
	)
	require.NoError(t, err)
	return payload
}

func TestSlackInboxCanonicalMessageAndMentionRouting(t *testing.T) {
	connection := slackInboxTestConnection()
	message := slack.Event{
		Type:        "message",
		User:        "U123",
		Channel:     "C123",
		ChannelType: "channel",
		Text:        "<@UBOT> please review",
		TS:          "1.2",
	}
	ordinary, ok, err := NormalizeSlackAppEvent(connection, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, ordinary.Event.Mentioned)
	message.Type = "app_mention"
	message.ChannelType = ""
	mention, ok, err := NormalizeSlackAppEvent(connection, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ordinary.SemanticKey, mention.SemanticKey)
	require.Equal(t, "slack:message:T123:C123:1.2", mention.SemanticKey)
	require.Equal(t, ordinary.Event, mention.Event)
	message.Files = []slack.File{{ID: "F123"}}
	withFiles, ok, err := NormalizeSlackAppEvent(connection, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ordinary.SemanticKey, withFiles.Sibling.Key)
	require.Equal(t, ordinary.Sibling.Key, withFiles.SemanticKey)
	require.NotEmpty(t, withFiles.Sibling.AttachmentNotice)

	message.Type = "message"
	message.Text = "ordinary channel root"
	message.ChannelType = "channel"
	root, ok, err := NormalizeSlackAppEvent(connection, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, root.Event.MatchesLauncher("mention"))
	message.ThreadTS = "1.0"
	reply, ok, err := NormalizeSlackAppEvent(connection, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "1.0", reply.Event.Scope.Slack.ThreadTS)
	require.Equal(t, executionstore.DeliveryModeSteering, reply.DeliveryMode)
	require.True(t, reply.CancelOpenInteractions)
	message.Channel = "D123"
	message.ChannelType = "im"
	message.ThreadTS = ""
	dm, ok, err := NormalizeSlackAppEvent(connection, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, dm.Event.Mentioned)
	kind, ref, err := dm.Event.Scope.Conversation()
	require.NoError(t, err)
	require.Equal(t, "dm", kind)
	require.Equal(t, "D123", ref)
}

func TestSlackInboxRejectsIdentityAndIgnoresBotOrMutation(t *testing.T) {
	connection := slackInboxTestConnection()
	base := slack.Event{Type: "message", User: "U123", Channel: "C123", TS: "1.2", ChannelType: "channel"}
	for _, change := range []func(*slack.Event){
		func(e *slack.Event) { e.User = "UBOT" },
		func(e *slack.Event) { e.BotID = "B123" },
		func(e *slack.Event) { e.SourceTeam = "TOTHER" },
		func(e *slack.Event) { e.Subtype = "message_changed" },
	} {
		event := base
		change(&event)
		_, ok, err := NormalizeSlackAppEvent(connection, slackInboxTestPayload(t, event))
		require.NoError(t, err)
		require.False(t, ok)
	}
	connection.ProviderTenantID = "TOTHER"
	_, _, err := NormalizeSlackAppEvent(connection, slackInboxTestPayload(t, base))
	require.Error(t, err)
}

func TestSlackInboxFileShareConversationAndMention(t *testing.T) {
	for _, tc := range []struct {
		name, channel, channelType, text, thread, kind, ref string
		mentioned                                           bool
	}{
		{"dm", "D123", "im", "", "", "dm", "D123", true},
		{"root without mention", "C123", "channel", "file", "", "thread", "C123:1.2", false},
		{"root mention", "C123", "channel", "<@UBOT> file", "", "thread", "C123:1.2", true},
		{"thread reply", "C123", "channel", "file", "1.0", "thread", "C123:1.0", false},
		{"labeled thread mention", "C123", "group", "<@UBOT|Omnara> file", "1.0", "thread", "C123:1.0", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			event := slack.Event{
				Type: "message", Subtype: "file_share", User: "U123", TS: "1.2",
				Channel: tc.channel, ChannelType: tc.channelType, Text: tc.text, ThreadTS: tc.thread,
				Files: []slack.File{{ID: "F123"}},
			}
			normalized, ok, err := NormalizeSlackAppEvent(slackInboxTestConnection(), slackInboxTestPayload(t, event))
			require.NoError(t, err)
			require.True(t, ok)
			kind, ref, err := normalized.Event.Scope.Conversation()
			require.NoError(t, err)
			require.Equal(t, tc.kind, kind)
			require.Equal(t, tc.ref, ref)
			require.Equal(t, tc.mentioned, normalized.Event.MatchesLauncher("mention"))
			require.Equal(t, executionstore.DeliveryModeSteering, normalized.DeliveryMode)
			require.True(t, normalized.CancelOpenInteractions)
		})
	}
}

type slackInboxTestAccess struct {
	mu         sync.Mutex
	connection integrationstore.IntegrationConnectionRecord
	version    uuid.UUID
	revoked    bool
	afterRead  func()
}

func (s *slackInboxTestAccess) ReadProjectAvailableSecretPayload(
	_ context.Context,
	input secretstore.ReadProjectAvailableSecretPayloadInput,
) (secretstore.SecretPayloadRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || input.OrgID != s.connection.OrgID || input.ProjectID != s.connection.ProjectID ||
		input.SecretID != s.connection.CredentialSecretID ||
		input.Kind != secrets.KindSlackAppCredentials {
		return secretstore.SecretPayloadRecord{}, storeerr.ErrUnauthorized
	}
	payload, err := slack.CredentialPayload(
		slack.AppCredentials{
			BotToken:      "test-token",
			ClientID:      "client",
			ClientSecret:  "client-secret",
			SigningSecret: "signing-secret",
		},
	)
	record := secretstore.SecretPayloadRecord{Payload: payload, CurrentVersionID: s.version}
	if s.afterRead != nil {
		s.afterRead()
	}
	return record, err
}

func (s *slackInboxTestAccess) GetProjectAvailableSecret(
	context.Context,
	uuid.UUID,
	uuid.UUID,
	uuid.UUID,
) (secretstore.ProjectSecretAccessRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked {
		return secretstore.ProjectSecretAccessRecord{}, storeerr.ErrUnauthorized
	}
	return secretstore.ProjectSecretAccessRecord{
		Secret: secretstore.SecretRecord{
			Kind:             secrets.KindSlackAppCredentials,
			CurrentVersionID: s.version,
			OwnerKind:        secretstore.SecretOwnerOrg,
		},
		Availability: secretstore.SecretAvailability{Source: secretstore.SecretAvailabilityGrant},
	}, nil
}

func (s *slackInboxTestAccess) GetIntegrationConnection(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (integrationstore.IntegrationConnectionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.connection, nil
}

func (s *slackInboxTestAccess) GetConversationDisplayName(
	context.Context, uuid.UUID, uuid.UUID, integrationstore.ConversationAddress,
) (string, error) {
	return "", nil
}

func TestSlackInboxEnrichmentUsesTypedProviderContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/users.info":
			_, _ = w.Write([]byte(`{"ok":true,"user":{"profile":{"display_name":"Alex"}}}`))
		case "/conversations.info":
			_, _ = w.Write([]byte(`{"ok":true,"channel":{"name":"reviews"}}`))
		case "/conversations.history":
			_, _ = w.Write([]byte(`{"ok":true,"messages":[{"user":"U123","text":"previous context","ts":"1.1"}]}`))
		default:
			t.Errorf("unexpected provider method %s", r.URL.Path)
			w.WriteHeader(http.StatusInternalServerError)
		}
	}))
	defer server.Close()
	connection := slackInboxTestConnection()
	access := &slackInboxTestAccess{connection: connection, version: uuid.New()}
	provider := NewSlackAppInboxProvider(
		slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		access,
		access,
		nil,
	)
	event := slack.Event{Type: "app_mention", User: "U123", Channel: "C123", TS: "1.2", Text: "<@UBOT> review"}
	expanded, err := provider.Expand(t.Context(), connection, slackInboxTestPayload(t, event))
	require.NoError(t, err)
	require.Len(t, expanded.Events, 1)
	input := expanded.Events[0]
	require.Equal(t, "reviews", input.DisplayName)
	require.NotNil(t, input.Actor.DisplayName)
	require.Equal(t, "Alex", *input.Actor.DisplayName)
	require.Contains(t, string(input.ContentBlocks), "previous context")
	require.NotContains(t, string(input.ContentBlocks), "send_integration_message")
	require.NotContains(t, string(input.Metadata), "test-token")
}

func TestSlackInboxDisplayMetadataUnicodeBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"user":{"profile":{"display_name":"Alex"}},"channel":{"name":"reviews"}}`))
	}))
	defer server.Close()
	for name, count := range map[string]int{"below": 511, "boundary": 512, "over": 513} {
		t.Run(name, func(t *testing.T) {
			connection := slackInboxTestConnection()
			access := &slackInboxTestAccess{connection: connection, version: uuid.New()}
			provider := NewSlackAppInboxProvider(
				slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				access,
				access,
				nil,
			)
			suffix := strings.Repeat("界", count-utf8.RuneCountInString("@Alex "))
			event := slack.Event{Type: "message", User: "U123", Channel: "C123", TS: "1.2", Text: "<@U123> " + suffix}
			expanded, err := provider.Expand(t.Context(), connection, slackInboxTestPayload(t, event))
			require.NoError(t, err)
			var blocks []struct {
				Text     string            `json:"text"`
				Metadata map[string]string `json:"metadata"`
			}
			require.NoError(t, json.Unmarshal(expanded.Events[0].ContentBlocks, &blocks))
			require.Len(t, blocks, 2)
			require.Contains(t, blocks[1].Text, "<@U123> (Alex)")
			display, exists := blocks[1].Metadata["omnara_display_text"]
			require.Equal(t, count <= 512, exists)
			if exists {
				require.Equal(t, "@Alex "+suffix, display)
				require.Equal(t, count, utf8.RuneCountInString(display))
				require.Greater(t, len(display), 512, "display limit counts Unicode code points, not bytes")
			}
		})
	}
}

func TestAppEventsDiscordGuildDiffersFromConnectionApplication(t *testing.T) {
	connection := integrationstore.IntegrationConnectionRecord{
		Provider:           "discord",
		ProviderTenantID:   "999",
		ProviderAccountRef: "888",
	}
	event := AppEvent{
		Event: appdefinition.Event{
			Scope: appdefinition.Scope{
				Discord: &appdefinition.DiscordScope{GuildID: "123", ChannelID: "456", ThreadID: "789"},
			},
			Kind: "message",
		},
		SemanticKey:   "discord:message:111",
		Actor:         executionstore.ActorParams{Provider: "discord", ProviderTenantID: "999", ProviderUserID: "777"},
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
	}
	requests, err := prepareAppEvents([]AppEvent{event}, connection)
	require.NoError(t, err)
	require.Contains(t, requests[0].scopes, integrationstore.ConversationAddress{Kind: "guild", Ref: "123"})
	event.Actor.ProviderTenantID = "123"
	_, err = prepareAppEvents([]AppEvent{event}, connection)
	require.Error(t, err, "actor tenant remains the bot application, not its event guild")
}

func TestSlackInboxRechecksConnectionAndGrantedCredentialAfterUnwrap(t *testing.T) {
	for _, change := range []string{"disabled", "reconfigured", "rotated", "grant_revoked"} {
		t.Run(change, func(t *testing.T) {
			connection := slackInboxTestConnection()
			access := &slackInboxTestAccess{connection: connection, version: uuid.New()}
			access.afterRead = func() {
				switch change {
				case "disabled":
					access.connection.State = "disabled"
				case "reconfigured":
					access.connection.UpdatedAt = access.connection.UpdatedAt.Add(time.Second)
				case "rotated":
					access.version = uuid.New()
				case "grant_revoked":
					access.revoked = true
				}
			}
			server := httptest.NewServer(
				http.HandlerFunc(
					func(w http.ResponseWriter, r *http.Request) { t.Error("provider I/O after authority changed") },
				),
			)
			defer server.Close()
			provider := NewSlackAppInboxProvider(
				slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				access,
				access,
				nil,
			)
			event := slack.Event{Type: "app_mention", User: "U123", Channel: "C123", TS: "1.2", Text: "review"}
			_, err := provider.Expand(t.Context(), connection, slackInboxTestPayload(t, event))
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
		})
	}
}

func TestSlackInboxRevocationBetweenEnrichmentRequests(t *testing.T) {
	for _, change := range []string{"connection", "credential", "grant"} {
		t.Run(change, func(t *testing.T) {
			connection := slackInboxTestConnection()
			access := &slackInboxTestAccess{connection: connection, version: uuid.New()}
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				access.mu.Lock()
				defer access.mu.Unlock()
				calls++
				if calls > 1 {
					t.Error("second request used revoked credentials")
				}
				switch change {
				case "connection":
					access.connection.State = "disabled"
				case "credential":
					access.version = uuid.New()
				case "grant":
					access.revoked = true
				}
				_, _ = w.Write([]byte(`{"ok":true,"messages":[]}`))
			}))
			defer server.Close()
			provider := NewSlackAppInboxProvider(
				slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				access,
				access,
				nil,
			)
			event := slack.Event{Type: "app_mention", User: "U123", Channel: "C123", TS: "1.2", Text: "review"}
			_, err := provider.Expand(t.Context(), connection, slackInboxTestPayload(t, event))
			require.ErrorIs(t, err, storeerr.ErrUnauthorized, "best-effort enrichment cannot hide lost authority")
			access.mu.Lock()
			require.Equal(t, 1, calls)
			access.mu.Unlock()
		})
	}
}
