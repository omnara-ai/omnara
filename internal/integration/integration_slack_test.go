package integration

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/integrationdefinition"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/require"
)

func slackInboxTestIntegration() integrationstore.ProjectIntegrationRecord {
	return integrationstore.ProjectIntegrationRecord{
		ID:                 uuid.New(),
		OrgID:              uuid.New(),
		ProjectID:          uuid.New(),
		CredentialSecretID: uuid.New(),
		State:              integrationstore.ProjectIntegrationStateActive,
		UpdatedAt:          time.Now(),
		Provider:           "slack",
		ProviderTenantID:   "T123",
		ProviderAccountRef: "A123",
		ProviderIdentity:   json.RawMessage(`{"bot_user_id":"UBOT"}`),
	}
}

func TestSlackInboxRoutesBeforeExpansion(t *testing.T) {
	for _, routeErr := range []error{nil, integrationstore.ErrIntegrationSelectionReserved} {
		provider := &SlackIntegrationInboxProvider{}
		called := false
		expansion, err := provider.ExpandRouted(t.Context(), slackInboxTestIntegration(), slackInboxTestPayload(t,
			slack.Event{Type: "message", Channel: "C123", TS: "2.0", ThreadTS: "1.0", User: "U123", Text: "reply"}),
			func(event IntegrationEvent) (bool, error) {
				called = true
				require.Equal(t, "1.0", event.Event.Scope.Slack.ThreadTS)
				return false, routeErr
			})
		require.True(t, called)
		require.ErrorIs(t, err, routeErr)
		require.Empty(t, expansion.Events)
	}
}

func TestSlackInboxBroadcastUsesOriginalThreadAndMessageIdentity(t *testing.T) {
	definition, _ := integrationdefinition.Lookup(integrationdefinition.SlackThread)
	integration := slackInboxTestIntegration()
	event := slack.Event{Type: "message", Subtype: "thread_broadcast", Channel: "C123",
		TS: "2.0", ThreadTS: "1.0", User: "U123", Text: "ordinary reply"}
	broadcast, ok, err := NormalizeSlackIntegrationEvent(integration, slackInboxTestPayload(t, event))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, &integrationdefinition.SlackScope{ChannelID: "C123", ThreadTS: "1.0"}, broadcast.Event.Scope.Slack)
	require.False(t, definition.MatchesLauncher(broadcast.Event, "mention"))
	event.Subtype = ""
	ordinary, ok, err := NormalizeSlackIntegrationEvent(integration, slackInboxTestPayload(t, event))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ordinary.SemanticKey, broadcast.SemanticKey)
	require.Equal(t, ordinary.Sibling, broadcast.Sibling)
	event.Subtype, event.ThreadTS, event.Text = "thread_broadcast", "", "<@UBOT> malformed broadcast"
	_, ok, err = NormalizeSlackIntegrationEvent(integration, slackInboxTestPayload(t, event))
	require.NoError(t, err)
	require.False(t, ok, "a broadcast without its thread must not turn into a root mention")
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
	definition, _ := integrationdefinition.Lookup(integrationdefinition.SlackThread)
	integrationSetup := slackInboxTestIntegration()
	message := slack.Event{
		Type:        "message",
		User:        "U123",
		Channel:     "C123",
		ChannelType: "channel",
		Text:        "<@UBOT> please review",
		TS:          "1.2",
	}
	ordinary, ok, err := NormalizeSlackIntegrationEvent(integrationSetup, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, ordinary.Event.Mentioned)
	message.Type = "app_mention"
	message.ChannelType = ""
	mention, ok, err := NormalizeSlackIntegrationEvent(integrationSetup, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ordinary.SemanticKey, mention.SemanticKey)
	require.Equal(t, "slack:message:T123:C123:1.2", mention.SemanticKey)
	require.Equal(t, ordinary.Event, mention.Event)
	message.Files = []slack.File{{ID: "F123"}}
	withFiles, ok, err := NormalizeSlackIntegrationEvent(integrationSetup, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, ordinary.SemanticKey, withFiles.Sibling.Key)
	require.Equal(t, ordinary.Sibling.Key, withFiles.SemanticKey)
	require.NotEmpty(t, withFiles.Sibling.AttachmentNotice)

	message.Type = "message"
	message.Text = "ordinary channel root"
	message.ChannelType = "channel"
	root, ok, err := NormalizeSlackIntegrationEvent(integrationSetup, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.False(t, definition.MatchesLauncher(root.Event, "mention"))
	message.ThreadTS = "1.0"
	reply, ok, err := NormalizeSlackIntegrationEvent(integrationSetup, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.Equal(t, "1.0", reply.Event.Scope.Slack.ThreadTS)
	require.Equal(t, executionstore.DeliveryModeSteering, reply.DeliveryMode)
	require.True(t, reply.CancelOpenInteractions)
	message.Channel = "D123"
	message.ChannelType = "im"
	message.ThreadTS = ""
	dm, ok, err := NormalizeSlackIntegrationEvent(integrationSetup, slackInboxTestPayload(t, message))
	require.NoError(t, err)
	require.True(t, ok)
	require.True(t, dm.Event.Mentioned)
	kind, ref, err := dm.Event.Scope.Conversation()
	require.NoError(t, err)
	require.Equal(t, "dm", kind)
	require.Equal(t, "D123", ref)
}

func TestSlackInboxRejectsIdentityAndIgnoresBotOrMutation(t *testing.T) {
	integrationSetup := slackInboxTestIntegration()
	base := slack.Event{Type: "message", User: "U123", Channel: "C123", TS: "1.2", ChannelType: "channel"}
	for _, change := range []func(*slack.Event){
		func(e *slack.Event) { e.User = "UBOT" },
		func(e *slack.Event) { e.BotID = "B123" },
		func(e *slack.Event) { e.SourceTeam = "TOTHER" },
		func(e *slack.Event) { e.Subtype = "message_changed" },
	} {
		event := base
		change(&event)
		_, ok, err := NormalizeSlackIntegrationEvent(integrationSetup, slackInboxTestPayload(t, event))
		require.NoError(t, err)
		require.False(t, ok)
	}
	integrationSetup.ProviderTenantID = "TOTHER"
	_, _, err := NormalizeSlackIntegrationEvent(integrationSetup, slackInboxTestPayload(t, base))
	require.Error(t, err)
}

func TestSlackInboxFileShareConversationAndMention(t *testing.T) {
	definition, _ := integrationdefinition.Lookup(integrationdefinition.SlackThread)
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
			normalized, ok, err := NormalizeSlackIntegrationEvent(slackInboxTestIntegration(), slackInboxTestPayload(t, event))
			require.NoError(t, err)
			require.True(t, ok)
			kind, ref, err := normalized.Event.Scope.Conversation()
			require.NoError(t, err)
			require.Equal(t, tc.kind, kind)
			require.Equal(t, tc.ref, ref)
			require.Equal(t, tc.mentioned, definition.MatchesLauncher(normalized.Event, "mention"))
			require.Equal(t, executionstore.DeliveryModeSteering, normalized.DeliveryMode)
			require.True(t, normalized.CancelOpenInteractions)
		})
	}
}

type slackInboxTestAccess struct {
	mu               sync.Mutex
	integrationSetup integrationstore.ProjectIntegrationRecord
	version          uuid.UUID
	token            string
	revoked          bool
	afterRead        func()
}

func (s *slackInboxTestAccess) ReadProjectAvailableSecretPayload(
	_ context.Context,
	input secretstore.ReadProjectAvailableSecretPayloadInput,
) (secretstore.SecretPayloadRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.revoked || input.OrgID != s.integrationSetup.OrgID || input.ProjectID != s.integrationSetup.ProjectID ||
		input.SecretID != s.integrationSetup.CredentialSecretID ||
		input.Kind != secrets.KindSlackAppCredentials {
		return secretstore.SecretPayloadRecord{}, storeerr.ErrUnauthorized
	}
	token := s.token
	if token == "" {
		token = "test-token"
	}
	payload, err := slack.CredentialPayload(
		slack.AppCredentials{
			BotToken:      token,
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

func (s *slackInboxTestAccess) GetProjectIntegration(
	context.Context,
	uuid.UUID,
	uuid.UUID,
) (integrationstore.ProjectIntegrationRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.integrationSetup, nil
}

func (s *slackInboxTestAccess) GetConversationDisplayName(
	context.Context, uuid.UUID, uuid.UUID, integrationstore.ConversationAddress,
) (string, error) {
	return "", nil
}

type slackActorNamesFunc func(context.Context, uuid.UUID, string, string, []string) (map[string]string, error)

func (f slackActorNamesFunc) ListActorDisplayNames(
	ctx context.Context, projectID uuid.UUID, provider, tenant string, users []string,
) (map[string]string, error) {
	return f(ctx, projectID, provider, tenant, users)
}

func TestSlackInboxEnrichmentUsesTypedProviderContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/auth.test":
			_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
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
	integrationSetup := slackInboxTestIntegration()
	access := &slackInboxTestAccess{integrationSetup: integrationSetup, version: uuid.New()}
	provider := NewSlackIntegrationInboxProvider(
		slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		access,
		access,
		nil,
	)
	event := slack.Event{Type: "app_mention", User: "U123", Channel: "C123", TS: "1.2", Text: "<@UBOT> review"}
	expanded, err := provider.Expand(t.Context(), integrationSetup, slackInboxTestPayload(t, event))
	require.NoError(t, err)
	require.Len(t, expanded.Events, 1)
	input := expanded.Events[0]
	require.Equal(t, "reviews", input.DisplayName)
	require.NotNil(t, input.Actor.DisplayName)
	require.Equal(t, "Alex", *input.Actor.DisplayName)
	require.Contains(t, string(input.ContentBlocks), "previous context")
	require.NotContains(t, string(input.Metadata), "test-token")

	provider.actors = slackActorNamesFunc(func(
		_ context.Context, projectID uuid.UUID, source, tenant string, users []string,
	) (map[string]string, error) {
		require.Equal(t, integrationSetup.ProjectID, projectID)
		require.Equal(t, executionstore.ActorProviderIntegration, source)
		require.Equal(t, input.Actor.ProviderTenantID, tenant)
		require.Contains(t, users, "U123")
		return map[string]string{"U123": "Cached Alex"}, nil
	})
	expanded, err = provider.Expand(t.Context(), integrationSetup, slackInboxTestPayload(t, event))
	require.NoError(t, err)
	require.Equal(t, "Cached Alex", *expanded.Events[0].Actor.DisplayName)
}

func TestSlackInboxDisplayMetadataUnicodeBoundary(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/auth.test" {
			_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"user":{"profile":{"display_name":"Alex"}},"channel":{"name":"reviews"}}`))
	}))
	defer server.Close()
	for name, count := range map[string]int{"below": 511, "boundary": 512, "over": 513} {
		t.Run(name, func(t *testing.T) {
			integrationSetup := slackInboxTestIntegration()
			access := &slackInboxTestAccess{integrationSetup: integrationSetup, version: uuid.New()}
			provider := NewSlackIntegrationInboxProvider(
				slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				access,
				access,
				nil,
			)
			suffix := strings.Repeat("界", count-utf8.RuneCountInString("@Alex "))
			event := slack.Event{Type: "message", User: "U123", Channel: "C123", TS: "1.2", Text: "<@U123> " + suffix}
			expanded, err := provider.Expand(t.Context(), integrationSetup, slackInboxTestPayload(t, event))
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

func TestIntegrationEventsDiscordGuildDiffersFromIntegrationApplication(t *testing.T) {
	integrationSetup := integrationstore.ProjectIntegrationRecord{
		IntegrationType:    integrationdefinition.DiscordThread,
		ID:                 uuid.New(),
		Provider:           "discord",
		ProviderTenantID:   "999",
		ProviderAccountRef: "888",
	}
	event := IntegrationEvent{
		Event: integrationdefinition.Event{
			Scope: integrationdefinition.Scope{
				Discord: &integrationdefinition.DiscordScope{GuildID: "123", ChannelID: "456", ThreadID: "789"},
			},
			Kind: "message",
		},
		SemanticKey:   "discord:message:111",
		Actor:         integrationTestActor(t, integrationSetup.ID, "777"),
		ContentBlocks: json.RawMessage(`[{"type":"text","text":"hello"}]`),
	}
	requests, err := prepareIntegrationEvents([]IntegrationEvent{event}, integrationSetup)
	require.NoError(t, err)
	require.Equal(t, "123", requests[0].event.Event.Scope.Discord.GuildID)
	require.Equal(t, integrationstore.ConversationAddress{Kind: "thread", Ref: "456:789"}, requests[0].address)
}

func TestSlackInboxRechecksIntegrationSetupAndGrantedCredentialAfterUnwrap(t *testing.T) {
	for _, change := range []string{"disabled", "reconfigured", "rotated", "grant_revoked"} {
		t.Run(change, func(t *testing.T) {
			integrationSetup := slackInboxTestIntegration()
			access := &slackInboxTestAccess{integrationSetup: integrationSetup, version: uuid.New()}
			access.afterRead = func() {
				switch change {
				case "disabled":
					access.integrationSetup.State = integrationstore.ProjectIntegrationStateDisconnected
				case "reconfigured":
					access.integrationSetup.SetupRevision++
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
			provider := NewSlackIntegrationInboxProvider(
				slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				access,
				access,
				nil,
			)
			event := slack.Event{Type: "app_mention", User: "U123", Channel: "C123", TS: "1.2", Text: "review"}
			_, err := provider.Expand(t.Context(), integrationSetup, slackInboxTestPayload(t, event))
			require.ErrorIs(t, err, storeerr.ErrUnauthorized)
		})
	}
}

func TestSlackInboxRevocationBetweenEnrichmentRequests(t *testing.T) {
	for _, change := range []string{"integration", "credential", "grant"} {
		t.Run(change, func(t *testing.T) {
			integrationSetup := slackInboxTestIntegration()
			access := &slackInboxTestAccess{integrationSetup: integrationSetup, version: uuid.New()}
			var calls int
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth.test" {
					_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
					return
				}
				access.mu.Lock()
				defer access.mu.Unlock()
				calls++
				if calls > 1 {
					t.Error("second request used revoked credentials")
				}
				switch change {
				case "integration":
					access.integrationSetup.State = integrationstore.ProjectIntegrationStateDisconnected
				case "credential":
					access.version = uuid.New()
				case "grant":
					access.revoked = true
				}
				_, _ = w.Write([]byte(`{"ok":true,"messages":[]}`))
			}))
			defer server.Close()
			provider := NewSlackIntegrationInboxProvider(
				slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
				access,
				access,
				nil,
			)
			event := slack.Event{Type: "app_mention", User: "U123", Channel: "C123", TS: "1.2", Text: "review"}
			_, err := provider.Expand(t.Context(), integrationSetup, slackInboxTestPayload(t, event))
			require.ErrorIs(t, err, storeerr.ErrUnauthorized, "best-effort enrichment cannot hide lost authority")
			access.mu.Lock()
			require.Equal(t, 1, calls)
			access.mu.Unlock()
		})
	}
}

func TestSlackInboxSettingsEditPreservesSetupAccess(t *testing.T) {
	integration := slackInboxTestIntegration()
	access := &slackInboxTestAccess{integrationSetup: integration, version: uuid.New()}
	access.afterRead = func() { access.integrationSetup.UpdatedAt = integration.UpdatedAt.Add(time.Hour) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`))
	}))
	t.Cleanup(server.Close)
	provider := NewSlackIntegrationInboxProvider(
		slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}, access, access, nil)
	_, _, check, err := provider.requestAccess(t.Context(), integration)
	require.NoError(t, err)
	require.NoError(t, check(t.Context()))
}

func TestSlackInboxRotatedTokenMustRetainProviderIdentity(t *testing.T) {
	for _, identity := range []string{"same", "other_workspace", "other_bot", "non_bot"} {
		t.Run(identity, func(t *testing.T) {
			var providerCalls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/auth.test" {
					team, user, bot := "T123", "UBOT", "B123"
					if r.Header.Get("Authorization") == "Bearer rotated-token" {
						switch identity {
						case "other_workspace":
							team = "TOTHER"
						case "other_bot":
							user = "UOTHER"
						case "non_bot":
							bot = ""
						}
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "team_id": team, "user_id": user, "bot_id": bot})
					return
				}
				providerCalls.Add(1)
				_, _ = w.Write([]byte(`{"ok":true,"user":{"profile":{"display_name":"Alex"}},"channel":{"name":"help"}}`))
			}))
			t.Cleanup(server.Close)
			integration := slackInboxTestIntegration()
			access := &slackInboxTestAccess{integrationSetup: integration, version: uuid.New()}
			provider := NewSlackIntegrationInboxProvider(
				slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}, access, access, nil)
			payload := slackInboxTestPayload(t, slack.Event{Type: "message", User: "U123", Channel: "C123", TS: "1.2"})
			_, err := provider.Expand(t.Context(), integration, payload)
			require.NoError(t, err)
			access.mu.Lock()
			access.token, access.version = "rotated-token", uuid.New()
			access.mu.Unlock()
			providerCalls.Store(0)
			_, err = provider.Expand(t.Context(), integration, payload)
			if identity == "same" {
				require.NoError(t, err)
				require.Positive(t, providerCalls.Load())
			} else {
				require.Error(t, err)
				require.Zero(t, providerCalls.Load(), "mismatched token must not reach enrichment or downloads")
			}
		})
	}
}

func integrationTestActor(t *testing.T, integrationID uuid.UUID, userID string) executionstore.ActorParams {
	t.Helper()
	actor, err := executionstore.IntegrationActorParams(integrationID, userID, nil)
	require.NoError(t, err)
	return actor
}
