package integration

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/appdefinition"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/integration/discord"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/secretstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func discordInboxConnection() integrationstore.IntegrationConnectionRecord {
	return integrationstore.IntegrationConnectionRecord{
		ID: uuid.New(), OrgID: uuid.New(), ProjectID: uuid.New(), CredentialSecretID: uuid.New(),
		Provider: "discord", ProviderTenantID: "11", ProviderAccountRef: "22", UpdatedAt: time.Now(),
		State: integrationstore.IntegrationConnectionStateActive, ProviderAgentDisplayName: "Helper",
	}
}

func discordInboxMessageFixture() discord.Message {
	return discord.Message{ID: "500", ChannelID: "300", GuildID: "100", Type: 0,
		Author:  discord.User{ID: "33", Username: "alex", GlobalName: "Alex"},
		Content: "<@22> hello", Mentions: []discord.User{{ID: "22", Bot: true}}, Timestamp: "2026-09-18T00:00:00Z"}
}

func discordInboxPayload(t *testing.T, message discord.Message) []byte {
	t.Helper()
	data, err := json.Marshal(message)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(discord.Dispatch{Type: "MESSAGE_CREATE", Sequence: 17, Data: data})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestDiscordInboxNormalizesMentionAndThreadReply(t *testing.T) {
	t.Parallel()
	connection := discordInboxConnection()
	message := discordInboxMessageFixture()
	channel := discord.Channel{ID: "300", GuildID: "100", Type: 0, Name: "help"}
	root, ok, err := NormalizeDiscordAppEvent(connection, discordInboxPayload(t, message), channel)
	if err != nil || !ok {
		t.Fatalf("root: ok=%v err=%v", ok, err)
	}
	want := appdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"}
	if *root.Event.Scope.Discord != want || !root.Event.MatchesLauncher("mention") ||
		root.DeliveryMode != executionstore.DeliveryModeSteering || !root.CancelOpenInteractions ||
		root.Actor.ProviderTenantID != "11" || root.Actor.ProviderUserID != "33" || *root.Actor.DisplayName != "Alex" {
		t.Fatalf("normalized mention: %+v", root)
	}
	var metadata DiscordEventMetadata
	if json.Unmarshal(root.Metadata, &metadata) != nil || !metadata.ThreadStarter || metadata.SourceChannelID != "300" {
		t.Fatalf("metadata: %s", root.Metadata)
	}
	message.ID, message.ChannelID, message.Type = "501", "500", 19
	message.Content, message.Mentions = "ordinary reply", nil
	channel = discord.Channel{ID: "500", GuildID: "100", ParentID: "300", Type: 11, Name: "discussion"}
	reply, ok, err := NormalizeDiscordAppEvent(connection, discordInboxPayload(t, message), channel)
	if err != nil || !ok || *reply.Event.Scope.Discord != want || reply.Event.Mentioned ||
		reply.DeliveryMode != executionstore.DeliveryModeSteering || !reply.CancelOpenInteractions {
		t.Fatalf("reply: %+v ok=%v err=%v", reply, ok, err)
	}
	if root.SemanticKey == reply.SemanticKey {
		t.Fatal("different messages share semantic identity")
	}
	message.Thread = &discord.Channel{ID: "999", ParentID: "777", GuildID: "888", Type: 11}
	replayed, ok, err := NormalizeDiscordAppEvent(connection, discordInboxPayload(t, message), channel)
	if err != nil || !ok || replayed.SemanticKey != reply.SemanticKey || *replayed.Event.Scope.Discord != want {
		t.Fatalf("nested event thread altered scope: %+v %v", replayed, err)
	}
	addresses, err := reply.Event.RoutingAddresses(connection.ProviderTenantID)
	if err != nil || len(addresses) != 3 || addresses[2].Ref != "100" {
		t.Fatalf("guild routing inferred from App ID: %+v %v", addresses, err)
	}
}

func TestDiscordInboxIgnoresNonConversationalEvents(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*discord.Message)
	}{
		{"self", func(m *discord.Message) { m.Author.ID = "22" }},
		{"other bot", func(m *discord.Message) { m.Author.Bot = true }},
		{"webhook", func(m *discord.Message) { m.WebhookID = "88" }},
		{"system", func(m *discord.Message) { m.Type = 18 }},
		{"DM", func(m *discord.Message) { m.GuildID = "" }},
		{"ordinary root", func(m *discord.Message) { m.Mentions = nil }},
		{"text mention without native mention", func(m *discord.Message) {
			m.Mentions, m.Content = nil, "<@22> @everyone"
		}},
		{"mentions App instead of bot", func(m *discord.Message) { m.Mentions = []discord.User{{ID: "11"}} }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			m := discordInboxMessageFixture()
			tc.edit(&m)
			_, ok, err := NormalizeDiscordAppEvent(discordInboxConnection(), discordInboxPayload(t, m),
				discord.Channel{ID: "300", GuildID: "100", Type: 0})
			if err != nil || ok {
				t.Fatalf("ignored input: ok=%v err=%v", ok, err)
			}
		})
	}
}

func TestDiscordInboxRejectsUnprovenScope(t *testing.T) {
	t.Parallel()
	for _, channel := range []discord.Channel{
		{ID: "301", GuildID: "100", Type: 0},
		{ID: "300", GuildID: "999", Type: 0},
		{ID: "300", Type: 0},
		{ID: "300", GuildID: "100", Type: 11},
		{ID: "300", GuildID: "100", ParentID: "300", Type: 11},
		{ID: "300", GuildID: "100", ParentID: "../400", Type: 11},
		{ID: "300", GuildID: "100", Type: 2},
	} {
		if _, ok, err := NormalizeDiscordAppEvent(discordInboxConnection(),
			discordInboxPayload(t, discordInboxMessageFixture()), channel); err == nil || ok {
			t.Fatalf("accepted unproven channel: %+v", channel)
		}
	}
	for _, raw := range []string{"null", "[]", "{}", "{", `{"type":"MESSAGE_CREATE","sequence":-1}`,
		`{"type":"MESSAGE_CREATE","sequence":1,"data":{"id":"../500"}}`} {
		if _, _, err := discordInboxMessage(discordInboxConnection(), []byte(raw)); err == nil {
			t.Fatalf("accepted invalid payload %q", raw)
		}
	}
	for _, raw := range [][]byte{{0xff}, bytes.Repeat([]byte(" "), integrationstore.IntegrationInboxMaxPayloadBytes+1)} {
		if _, _, err := discordInboxMessage(discordInboxConnection(), raw); err == nil {
			t.Fatal("accepted invalid bytes")
		}
	}
}

type discordInboxFixture struct {
	mu         sync.Mutex
	connection integrationstore.IntegrationConnectionRecord
	version    uuid.UUID
	identity   discord.Identity
	channels   map[string]discord.Channel
	message    discord.Message
	contents   map[string][]byte
	requests   []string
	posts      int
	revoked    bool
	afterRead  func()
	override   func(http.ResponseWriter, *http.Request) bool
}

func (f *discordInboxFixture) GetIntegrationConnection(
	_ context.Context, projectID, connectionID uuid.UUID,
) (integrationstore.IntegrationConnectionRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if projectID != f.connection.ProjectID || connectionID != f.connection.ID {
		return integrationstore.IntegrationConnectionRecord{}, storeerr.ErrUnauthorized
	}
	return f.connection, nil
}

func (f *discordInboxFixture) ReadProjectAvailableSecretPayload(
	_ context.Context, input secretstore.ReadProjectAvailableSecretPayloadInput,
) (secretstore.SecretPayloadRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if input.OrgID != f.connection.OrgID || input.ProjectID != f.connection.ProjectID ||
		input.SecretID != f.connection.CredentialSecretID || input.Kind != secrets.KindGeneric || f.revoked {
		return secretstore.SecretPayloadRecord{}, storeerr.ErrUnauthorized
	}
	result := secretstore.SecretPayloadRecord{
		CurrentVersionID: f.version, Payload: secrets.Payload{secrets.KeyValue: "bot-token"},
	}
	if f.afterRead != nil {
		f.afterRead()
	}
	return result, nil
}

func (f *discordInboxFixture) GetProjectAvailableSecret(
	_ context.Context, orgID, projectID, secretID uuid.UUID,
) (secretstore.ProjectSecretAccessRecord, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.revoked || orgID != f.connection.OrgID || projectID != f.connection.ProjectID ||
		secretID != f.connection.CredentialSecretID {
		return secretstore.ProjectSecretAccessRecord{}, storeerr.ErrUnauthorized
	}
	return secretstore.ProjectSecretAccessRecord{
		Secret: secretstore.SecretRecord{
			Kind: secrets.KindGeneric, CurrentVersionID: f.version, OwnerKind: secretstore.SecretOwnerOrg,
		},
		Availability: secretstore.SecretAvailability{Source: secretstore.SecretAvailabilityGrant},
	}, nil
}

type discordInboxTransport func(*http.Request) (*http.Response, error)

func (f discordInboxTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func newDiscordInboxFixture(t *testing.T) (*discordInboxFixture, *DiscordAppInboxProvider) {
	t.Helper()
	f := &discordInboxFixture{connection: discordInboxConnection(), version: uuid.New(),
		identity: discord.Identity{ApplicationID: "11", BotUserID: "22"}, message: discordInboxMessageFixture(),
		channels: map[string]discord.Channel{
			"300": {ID: "300", GuildID: "100", Type: 0, Name: "help"},
			"400": {ID: "400", GuildID: "100", ParentID: "300", Type: 11, Name: "thread"},
		}, contents: map[string][]byte{},
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		f.requests = append(f.requests, r.Method+" "+r.URL.Path)
		if strings.HasPrefix(r.URL.Path, "/attachments/") {
			if r.Header.Get("Authorization") != "" {
				t.Error("bot token sent to CDN")
			}
		} else if r.Header.Get("Authorization") != "Bot bot-token" {
			t.Error("API bot authorization missing")
		}
		if f.override != nil && f.override(w, r) {
			return
		}
		w.Header().Set("Content-Type", "application/json")
		var result any
		switch r.URL.Path {
		case "/api/v10/users/@me":
			result = discord.User{ID: f.identity.BotUserID, Bot: true}
		case "/api/v10/applications/@me":
			result = map[string]string{"id": f.identity.ApplicationID}
		default:
			parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/v10/"), "/")
			switch {
			case len(parts) == 2 && parts[0] == "channels":
				channel, ok := f.channels[parts[1]]
				if !ok {
					w.WriteHeader(http.StatusNotFound)
					return
				}
				result = channel
			case len(parts) == 4 && parts[0] == "channels" && parts[2] == "messages":
				if parts[1] != f.message.ChannelID || parts[3] != f.message.ID {
					t.Errorf("wrong source message requested: %s", r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
					return
				}
				result = f.message
			case len(parts) == 5 && parts[4] == "threads" && r.Method == http.MethodPost:
				f.posts++
				thread := discord.Channel{ID: parts[3], GuildID: "100", ParentID: parts[1], Type: 11, Name: "conversation"}
				f.channels[thread.ID] = thread
				result = thread
			case strings.HasPrefix(r.URL.Path, "/attachments/"):
				cdnParts := strings.Split(r.URL.Path, "/")
				_, _ = w.Write(f.contents[cdnParts[3]])
				return
			default:
				t.Errorf("unexpected provider request: %s %s", r.Method, r.URL.Path)
				w.WriteHeader(http.StatusNotFound)
				return
			}
		}
		if err := json.NewEncoder(w).Encode(result); err != nil {
			t.Error(err)
		}
	}))
	t.Cleanup(server.Close)
	base, err := url.Parse(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	transport := server.Client().Transport
	client := &http.Client{Transport: discordInboxTransport(func(r *http.Request) (*http.Response, error) {
		if deadline, ok := r.Context().Deadline(); !ok || time.Until(deadline) > discord.OperationTimeout {
			t.Error("unbounded provider request")
		}
		if r.URL.Host == "cdn.discordapp.com" || r.URL.Host == "media.discordapp.net" {
			r = r.Clone(r.Context())
			u := *r.URL
			u.Scheme, u.Host = base.Scheme, base.Host
			r.URL = &u
		} else if r.URL.Host != base.Host {
			return nil, fmt.Errorf("test refused an external provider call")
		}
		return transport.RoundTrip(r)
	})}
	p := NewDiscordAppInboxProvider(discord.Config{HTTPClient: client, APIURL: server.URL + "/api/v10"}, f, f)
	return f, p
}

func TestDiscordInboxExpansionIsReadOnlyAndReplayStable(t *testing.T) {
	t.Parallel()
	f, p := newDiscordInboxFixture(t)
	raw := discordInboxPayload(t, f.message)
	expansion, err := p.Expand(t.Context(), f.connection, raw)
	if err != nil || len(expansion.Events) != 1 || f.posts != 0 {
		t.Fatalf("expansion: %+v %v posts=%d", expansion, err, f.posts)
	}
	var dispatch discord.Dispatch
	if err := json.Unmarshal(raw, &dispatch); err != nil {
		t.Fatal(err)
	}
	dispatch.Sequence = 999
	raw, err = json.Marshal(dispatch)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := p.Expand(t.Context(), f.connection, raw)
	if err != nil || len(replay.Events) != 1 || replay.Events[0].SemanticKey != expansion.Events[0].SemanticKey {
		t.Fatalf("replay: %+v %v", replay, err)
	}
	message := f.message
	message.Mentions = nil
	ignored, err := p.Expand(t.Context(), f.connection, discordInboxPayload(t, message))
	if err != nil || len(ignored.Events) != 0 {
		t.Fatalf("ordinary root: %+v %v", ignored, err)
	}
	for _, request := range f.requests {
		if !strings.HasPrefix(request, "GET ") {
			t.Fatalf("expansion mutated Discord: %s", request)
		}
	}
}

func TestDiscordInboxAccessIsRevalidated(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name string
		edit func(*discordInboxFixture)
	}{
		{"connection disabled", func(f *discordInboxFixture) {
			f.connection.State = integrationstore.IntegrationConnectionStateDisabled
		}},
		{"connection revision", func(f *discordInboxFixture) {
			f.connection.UpdatedAt = f.connection.UpdatedAt.Add(time.Second)
		}},
		{"secret revoked", func(f *discordInboxFixture) { f.revoked = true }},
		{"secret version", func(f *discordInboxFixture) { f.version = uuid.New() }},
		{"app identity", func(f *discordInboxFixture) { f.connection.ProviderTenantID = "111" }},
		{"bot identity", func(f *discordInboxFixture) { f.connection.ProviderAccountRef = "222" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			f, p := newDiscordInboxFixture(t)
			connection := f.connection
			f.afterRead = func() { tc.edit(f) }
			_, err := p.Expand(t.Context(), connection, discordInboxPayload(t, f.message))
			if !errors.Is(err, storeerr.ErrUnauthorized) || len(f.requests) != 0 {
				t.Fatalf("authority: err=%v requests=%v", err, f.requests)
			}
		})
	}
}

func TestDiscordInboxWrongTokenIdentityAndRetryFence(t *testing.T) {
	t.Parallel()
	t.Run("wrong token App", func(t *testing.T) {
		t.Parallel()
		f, p := newDiscordInboxFixture(t)
		f.identity.ApplicationID = "999"
		_, err := p.Expand(t.Context(), f.connection, discordInboxPayload(t, f.message))
		var apiErr *discord.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != discord.ScopeMismatch || len(f.requests) != 2 {
			t.Fatalf("token identity: %v requests=%v", err, f.requests)
		}
	})
	t.Run("rotation before safe retry", func(t *testing.T) {
		t.Parallel()
		f, p := newDiscordInboxFixture(t)
		f.override = func(w http.ResponseWriter, r *http.Request) bool {
			if r.URL.Path != "/api/v10/channels/300" {
				return false
			}
			f.version = uuid.New()
			w.WriteHeader(http.StatusServiceUnavailable)
			return true
		}
		_, err := p.Expand(t.Context(), f.connection, discordInboxPayload(t, f.message))
		if !errors.Is(err, storeerr.ErrUnauthorized) || len(f.requests) != 3 {
			t.Fatalf("retry fence: %v requests=%v", err, f.requests)
		}
	})
}

func TestDiscordInboxPrepareCreatesOneThreadOnlyAfterAuthority(t *testing.T) {
	t.Parallel()
	f, p := newDiscordInboxFixture(t)
	raw := discordInboxPayload(t, f.message)
	scope := appdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"}
	if err := p.PrepareConversation(t.Context(), f.connection, raw, scope, nil); err == nil || len(f.requests) != 0 {
		t.Fatalf("missing authority: %v requests=%v", err, f.requests)
	}
	var checks atomic.Int32
	authority := func(context.Context) error { checks.Add(1); return nil }
	for range 2 {
		if err := p.PrepareConversation(t.Context(), f.connection, raw, scope, authority); err != nil {
			t.Fatal(err)
		}
	}
	if f.posts != 1 || int(checks.Load()) < len(f.requests) {
		t.Fatalf("thread preparation: posts=%d checks=%d requests=%d", f.posts, checks.Load(), len(f.requests))
	}
	wrong := scope
	wrong.ThreadID = "600"
	err := p.PrepareConversation(t.Context(), f.connection, raw, wrong, authority)
	if !errors.Is(err, storeerr.ErrUnauthorized) {
		t.Fatalf("frozen scope mismatch: %v", err)
	}
	if f.posts != 1 {
		t.Fatal("scope mismatch created a thread")
	}
}

func TestDiscordInboxPrepareRevocationCancelsMutation(t *testing.T) {
	t.Parallel()
	f, p := newDiscordInboxFixture(t)
	var revoked atomic.Bool
	f.override = func(_ http.ResponseWriter, r *http.Request) bool {
		if r.URL.Path == "/api/v10/channels/500" {
			revoked.Store(true)
		}
		return false
	}
	sentinel := errors.New("frozen recipient lost authority")
	err := p.PrepareConversation(t.Context(), f.connection, discordInboxPayload(t, f.message),
		appdefinition.DiscordScope{GuildID: "100", ChannelID: "300", ThreadID: "500"},
		func(context.Context) error {
			if revoked.Load() {
				return sentinel
			}
			return nil
		})
	if !errors.Is(err, sentinel) || f.posts != 0 {
		t.Fatalf("revoked mutation: err=%v posts=%d", err, f.posts)
	}
}

func (f *discordInboxFixture) attach(id string, content []byte, mediaType string) {
	f.contents[id] = content
	f.message.Attachments = append(f.message.Attachments, discord.Attachment{ID: id, Filename: "note-" + id + ".txt",
		Size: int64(len(content)), ContentType: mediaType,
		URL: "https://cdn.discordapp.com/attachments/" + f.message.ChannelID + "/" + id + "/note.txt?refreshed=true"})
}

type discordInboxArtifacts struct{ uploaded map[uuid.UUID][]byte }

func (f *discordInboxArtifacts) PreparedArtifactUploaded(
	_ context.Context, _ uuid.UUID, artifact artifactstore.PreparedArtifact,
) (bool, error) {
	return f.uploaded[artifact.ID] != nil, nil
}

func (f *discordInboxArtifacts) UploadPreparedArtifact(
	_ context.Context, _ uuid.UUID, artifact artifactstore.PreparedArtifact, content []byte,
) error {
	if blobstore.ContentDigest(content) != artifact.Digest || int64(len(content)) != artifact.SizeBytes {
		return storeerr.ErrIdempotencyConflict
	}
	f.uploaded[artifact.ID] = bytes.Clone(content)
	return nil
}

func TestDiscordInboxAttachmentsUseExistingFrozenArtifactFlow(t *testing.T) {
	t.Parallel()
	f, p := newDiscordInboxFixture(t)
	f.attach("701", []byte("hello"), "text/plain; charset=utf-8")
	captured := f.message
	captured.Attachments = append([]discord.Attachment(nil), f.message.Attachments...)
	captured.Attachments[0].URL = "https://untrusted.invalid/must-not-be-fetched"
	raw := discordInboxPayload(t, captured)
	expansion, err := p.Expand(t.Context(), f.connection, raw)
	if err != nil || len(expansion.Events) != 1 || len(expansion.Events[0].Files) != 1 {
		t.Fatalf("file expansion: %+v %v", expansion, err)
	}
	event := expansion.Events[0]
	expected := event.Files[0].Expected
	if expected == nil || expected.Validate() != nil || expected.Digest != blobstore.ContentDigest([]byte("hello")) ||
		expected.ContentType != "text/plain" || string(expansion.Files["701"].Content) != "hello" {
		t.Fatalf("prepared bytes: %+v", expected)
	}
	_, files, err := appRecipientContent(event)
	if err != nil || len(files) != 1 || files[0].ArtifactID == expected.ID || files[0].Expected.ID != files[0].ArtifactID {
		t.Fatalf("per-recipient frozen identity: %+v %v", files, err)
	}
	artifacts := &discordInboxArtifacts{uploaded: map[uuid.UUID][]byte{}}
	consumer := &AppInboxConsumer{artifacts: artifacts}
	slot := AppInboxSlot{AgentID: uuid.New(), Files: files}
	prepared, err := consumer.prepareFiles(t.Context(), p, f.connection, raw, slot, expansion.Files)
	if err != nil || len(prepared) != 1 || string(artifacts.uploaded[files[0].ArtifactID]) != "hello" {
		t.Fatalf("artifact upload: %+v %v", prepared, err)
	}
	requestCount := len(f.requests)
	if _, err := consumer.prepareFiles(t.Context(), p, f.connection, raw, slot, map[string]AppInboxFile{}); err != nil ||
		len(f.requests) != requestCount {
		t.Fatalf("already uploaded recovery performed I/O: %v", err)
	}
	delete(artifacts.uploaded, files[0].ArtifactID)
	if _, err := consumer.prepareFiles(t.Context(), p, f.connection, raw, slot, map[string]AppInboxFile{}); err != nil {
		t.Fatalf("rehydrated frozen bytes: %v", err)
	}
	delete(artifacts.uploaded, files[0].ArtifactID)
	f.mu.Lock()
	f.contents["701"] = []byte("other")
	f.mu.Unlock()
	_, err = consumer.prepareFiles(t.Context(), p, f.connection, raw, slot, map[string]AppInboxFile{})
	if !errors.Is(err, storeerr.ErrIdempotencyConflict) || len(artifacts.uploaded) != 0 {
		t.Fatalf("changed content was admitted: %v", err)
	}
	requestCount = len(f.requests)
	if _, err := p.DownloadFile(t.Context(), f.connection, raw, "702"); err == nil || len(f.requests) != requestCount {
		t.Fatalf("uncaptured file caused I/O: %v", err)
	}
}

func TestDiscordInboxAttachmentBoundsAndRateLimit(t *testing.T) {
	t.Parallel()
	t.Run("bounded skips", func(t *testing.T) {
		t.Parallel()
		f, p := newDiscordInboxFixture(t)
		f.message.Content = ""
		f.message.Attachments = []discord.Attachment{{ID: "701", Size: discord.MaxFileBytes + 1}, {ID: "702", Size: 0}}
		expansion, err := p.Expand(t.Context(), f.connection, discordInboxPayload(t, f.message))
		if err != nil || len(expansion.Events) != 1 || len(expansion.Files) != 0 || len(f.requests) != 3 {
			t.Fatalf("skip expansion: %+v %v requests=%v", expansion, err, f.requests)
		}
		if !strings.Contains(string(expansion.Events[0].ContentBlocks), "too_large") {
			t.Fatal("omitted file was silent")
		}
	})
	t.Run("download rate limited", func(t *testing.T) {
		t.Parallel()
		f, p := newDiscordInboxFixture(t)
		f.attach("701", []byte("hello"), "text/plain")
		f.override = func(w http.ResponseWriter, r *http.Request) bool {
			if !strings.HasPrefix(r.URL.Path, "/attachments/") {
				return false
			}
			w.WriteHeader(http.StatusTooManyRequests)
			_, _ = w.Write([]byte(`{"retry_after":60}`))
			return true
		}
		expansion, err := p.Expand(t.Context(), f.connection, discordInboxPayload(t, f.message))
		var apiErr *discord.APIError
		if !errors.As(err, &apiErr) || apiErr.Code != discord.RateLimited || apiErr.RetryAfter != time.Minute ||
			len(expansion.Events) != 0 {
			t.Fatalf("rate limit silently omitted file: %+v %v", expansion, err)
		}
	})
}

func TestDiscordInboxAttachmentRecoveryRefusesChangedMetadata(t *testing.T) {
	t.Parallel()
	f, p := newDiscordInboxFixture(t)
	f.attach("701", []byte("hello"), "text/plain")
	raw := discordInboxPayload(t, f.message)
	f.message.Attachments[0].Filename = "different.txt"
	if _, err := p.DownloadFile(t.Context(), f.connection, raw, "701"); !errors.Is(err, storeerr.ErrIdempotencyConflict) {
		t.Fatalf("changed file metadata: %v", err)
	}
	requestCount := len(f.requests)
	if _, err := p.DownloadFile(t.Context(), f.connection, raw, "../701"); err == nil || len(f.requests) != requestCount {
		t.Fatalf("unsafe file ID: %v", err)
	}
}

func TestDiscordInboxRevocationAfterDownloadDoesNotPublishEvent(t *testing.T) {
	t.Parallel()
	f, p := newDiscordInboxFixture(t)
	f.attach("701", []byte("hello"), "text/plain")
	f.override = func(_ http.ResponseWriter, r *http.Request) bool {
		if strings.HasPrefix(r.URL.Path, "/attachments/") {
			f.revoked = true
		}
		return false
	}
	expansion, err := p.Expand(t.Context(), f.connection, discordInboxPayload(t, f.message))
	if !errors.Is(err, storeerr.ErrUnauthorized) || len(expansion.Events) != 0 || len(expansion.Files) != 0 {
		t.Fatalf("revoked expansion published: %+v %v", expansion, err)
	}
}

func TestDiscordInboxMediaBudgets(t *testing.T) {
	t.Parallel()
	t.Run("total bytes", func(t *testing.T) {
		t.Parallel()
		f, p := newDiscordInboxFixture(t)
		content := bytes.Repeat([]byte("a"), discord.MaxFileBytes)
		for _, id := range []string{"701", "702", "703"} {
			f.attach(id, content, "text/plain")
		}
		f.attach("704", []byte("too much"), "text/plain")
		expansion, err := p.Expand(t.Context(), f.connection, discordInboxPayload(t, f.message))
		if err != nil || len(expansion.Files) != 3 || len(expansion.Events) != 1 {
			t.Fatalf("bounded media: files=%d events=%d err=%v", len(expansion.Files), len(expansion.Events), err)
		}
		for _, request := range f.requests {
			if strings.Contains(request, "/704/") {
				t.Fatal("downloaded past total byte budget")
			}
		}
	})
	t.Run("count and empty", func(t *testing.T) {
		t.Parallel()
		f, p := newDiscordInboxFixture(t)
		for i := range discord.MaxFiles + 1 {
			f.attach(fmt.Sprint(701+i), nil, "text/plain")
		}
		expansion, err := p.Expand(t.Context(), f.connection, discordInboxPayload(t, f.message))
		if err != nil || len(expansion.Files) != 0 || len(f.requests) != 3 ||
			!strings.Contains(string(expansion.Events[0].Metadata), "too_many_attachments") {
			t.Fatalf("attachment count: %+v %v requests=%v", expansion, err, f.requests)
		}
	})
	t.Run("unsupported MIME", func(t *testing.T) {
		t.Parallel()
		f, p := newDiscordInboxFixture(t)
		f.attach("701", []byte{'O', 'g', 'g', 'S', 0, 0, 0}, "audio/ogg")
		expansion, err := p.Expand(t.Context(), f.connection, discordInboxPayload(t, f.message))
		if err != nil || len(expansion.Events) != 1 || len(expansion.Files) != 0 ||
			!strings.Contains(string(expansion.Events[0].Metadata), "unsupported_media_type") {
			t.Fatalf("unsupported media: %+v %v", expansion, err)
		}
	})
}

func TestDiscordInboxThreadPreparationDoesNotMutateReplies(t *testing.T) {
	t.Parallel()
	f, p := newDiscordInboxFixture(t)
	f.message.ID, f.message.ChannelID, f.message.Mentions = "501", "400", nil
	raw := discordInboxPayload(t, f.message)
	expansion, err := p.Expand(t.Context(), f.connection, raw)
	if err != nil || len(expansion.Events) != 1 {
		t.Fatalf("thread reply: %+v %v", expansion, err)
	}
	if err := p.PrepareConversation(t.Context(), f.connection, raw, *expansion.Events[0].Event.Scope.Discord,
		func(context.Context) error { return nil }); err != nil || f.posts != 0 {
		t.Fatalf("reply preparation: err=%v posts=%d", err, f.posts)
	}
}

func TestDiscordInboxContextAndMissingContent(t *testing.T) {
	t.Parallel()
	f, p := newDiscordInboxFixture(t)
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := p.Expand(ctx, f.connection, discordInboxPayload(t, f.message)); !errors.Is(err, context.Canceled) ||
		len(f.requests) != 0 {
		t.Fatalf("canceled request: %v requests=%v", err, f.requests)
	}
	f.message.Content = ""
	if _, err := p.Expand(t.Context(), f.connection, discordInboxPayload(t, f.message)); err == nil ||
		!strings.Contains(err.Error(), "message-content intent") {
		t.Fatalf("missing message content silently accepted: %v", err)
	}
	unsupported := []byte(`{"type":"MESSAGE_UPDATE","sequence":9,"data":{}}`)
	count := len(f.requests)
	if expanded, err := p.Expand(t.Context(), f.connection, unsupported); err != nil ||
		len(expanded.Events) != 0 || len(f.requests) != count {
		t.Fatalf("mutation event was expanded: %+v %v", expanded, err)
	}
}
