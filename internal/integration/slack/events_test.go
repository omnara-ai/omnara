package slack

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"
)

type panicRoundTripper struct{}

func (panicRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	panic("transport failure")
}

func TestModelInputTextPartsIncludeSlackContext(t *testing.T) {
	tests := []struct {
		name        string
		event       Event
		route       InboundRoute
		newlyMapped bool
		history     string
		wantMessage string
		wantHidden  string
	}{
		{
			name: "dm",
			event: Event{
				Type:        "message",
				User:        "U123",
				Text:        "hello",
				Channel:     "D123",
				ChannelType: "im",
				TS:          "111.222",
			},
			route:       InboundRoute{ProviderRef: "D123", ProviderRefKind: "dm"},
			wantMessage: "hello",
			wantHidden:  "<@U123> (Ada) in Slack DM:\n",
		},
		{
			name: "new root mention with channel history",
			event: Event{
				Type:        "app_mention",
				User:        "U123",
				Text:        "<@U_BOT> run",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "111.222",
			},
			route:       InboundRoute{ProviderRef: "C123:111.222", ProviderRefKind: "thread"},
			newlyMapped: true,
			history:     "Recent Slack context:\n<@U999> (Grace): earlier channel note",
			wantMessage: "<@U_BOT> run",
			wantHidden: "The agent was mentioned in a Slack channel, so this message starts " +
				"a new Slack thread for communicating with the agent.\n\n" +
				"Recent Slack context:\n" +
				"<@U999> (Grace): earlier channel note\n\n" +
				"<@U123> (Ada) in <#C123>, thread 111.222:\n",
		},
		{
			name: "new thread mention with thread history",
			event: Event{
				Type:        "app_mention",
				User:        "U123",
				Text:        "<@U_BOT> use the prior context",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "222.333",
				ThreadTS:    "111.222",
			},
			route:       InboundRoute{ProviderRef: "C123:111.222", ProviderRefKind: "thread"},
			newlyMapped: true,
			history:     "Recent Slack context:\n<@U999> (Grace): earlier thread note",
			wantMessage: "<@U_BOT> use the prior context",
			wantHidden: "This message directly mentioned the agent inside an existing Slack thread.\n\n" +
				"Recent Slack context:\n" +
				"<@U999> (Grace): earlier thread note\n\n" +
				"<@U123> (Ada) in <#C123>, thread 111.222:\n",
		},
		{
			name: "already mapped thread reply",
			event: Event{
				Type:        "message",
				User:        "U123",
				Text:        "one more thing",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "333.444",
				ThreadTS:    "111.222",
			},
			route:       InboundRoute{ProviderRef: "C123:111.222", ProviderRefKind: "thread", AppendOnly: true},
			wantMessage: "one more thing",
			wantHidden: "This message was posted in a Slack thread that is already attached " +
				"to this agent. It may be part of the ongoing conversation even if it does not " +
				"directly mention the agent.\n\n" +
				"<@U123> (Ada) in <#C123>, thread 111.222:\n",
		},
		{
			name: "root mention without fetched history",
			event: Event{
				Type:        "app_mention",
				User:        "U123",
				Text:        "<@U_BOT> run",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "111.222",
			},
			route:       InboundRoute{ProviderRef: "C123:111.222", ProviderRefKind: "thread"},
			newlyMapped: true,
			wantMessage: "<@U_BOT> run",
			wantHidden: "The agent was mentioned in a Slack channel, so this message starts " +
				"a new Slack thread for communicating with the agent.\n\n" +
				"<@U123> (Ada) in <#C123>, thread 111.222:\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message, hidden := ModelInputTextParts(
				test.event,
				test.route,
				test.newlyMapped,
				test.history,
				DisplayLabels{Users: map[string]string{"U123": "Ada"}},
			)
			if message != test.wantMessage || hidden != test.wantHidden {
				t.Fatalf(
					"ModelInputTextParts = (%q, %q), want (%q, %q)",
					message,
					hidden,
					test.wantMessage,
					test.wantHidden,
				)
			}
		})
	}
}

func TestModelInputTextPartsRenderSlackNames(t *testing.T) {
	event := Event{
		Type:        "app_mention",
		User:        "U123",
		Text:        "<@U_BOT> ask <@U456> about <#C999>",
		Channel:     "C123",
		ChannelType: "channel",
		TS:          "111.222",
	}
	route := InboundRoute{ProviderRef: "C123:111.222", ProviderRefKind: "thread"}
	labels := DisplayLabels{
		Users: map[string]string{
			"U123":  "Ada",
			"U456":  "Ben",
			"U_BOT": "Omnara",
		},
		Channels: map[string]string{
			"C123": "general",
			"C999": "eng",
		},
	}
	message, hidden := ModelInputTextParts(event, route, true, "", labels)
	display := labels.RenderDisplayText(strings.TrimSpace(event.Text))
	wantHidden := "The agent was mentioned in a Slack channel, so this message starts a new Slack thread " +
		"for communicating with the agent.\n\n" +
		"<@U123> (Ada) in <#C123> (#general), thread 111.222:\n"
	wantMessage := "<@U_BOT> (Omnara) ask <@U456> (Ben) about <#C999> (#eng)"
	wantDisplay := "@Omnara ask @Ben about #eng"
	if message != wantMessage || hidden != wantHidden || display != wantDisplay {
		t.Fatalf(
			"Slack text parts = (%q, %q, %q), want (%q, %q, %q)",
			message,
			hidden,
			display,
			wantMessage,
			wantHidden,
			wantDisplay,
		)
	}
}

func TestFormatRecentContextRendersSlackReferences(t *testing.T) {
	labels := DisplayLabels{
		Users: map[string]string{
			"U456": "Ben",
			"U999": "Grace",
		},
		Channels: map[string]string{"C999": "eng"},
	}
	got := FormatRecentContext(
		[]HistoryMessage{
			{User: "U999", Text: "ask <@U456> about <#C999>", TS: "111.111"},
			{User: "U888", Text: "skip current", TS: "222.222"},
		},
		Event{TS: "222.222"},
		labels,
	)
	want := "Recent Slack context:\n<@U999> (Grace): ask <@U456> (Ben) about <#C999> (#eng)"
	if got != want {
		t.Fatalf("FormatRecentContext =\n%s\nwant\n%s", got, want)
	}
}

func TestInboundEventMetadataSerializesTypedFileResults(t *testing.T) {
	ordinal := 0
	raw, err := InboundEventMetadata(
		"slack",
		EventsEnvelope{
			EventID: "Ev1",
			Event: Event{
				Type:        "message",
				Subtype:     "file_share",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "111.222",
			},
		},
		InboundRoute{ProviderRef: "C123:111.222", ProviderRefKind: "thread"},
		"fetched",
		[]EventFileResult{
			{
				Ordinal:           &ordinal,
				FileID:            "F123",
				Name:              "pixel.png",
				Mimetype:          "image/png",
				DeclaredSizeBytes: 12,
				Content:           []byte("do not serialize"),
				ContentType:       "image/png",
				Filename:          "pixel.png",
				SizeBytes:         12,
				Status:            EventFileStatusStored,
			},
			{
				Status:     EventFileStatusSkipped,
				SkipReason: "too_many_attachments",
				Count:      2,
			},
		},
	)
	if err != nil {
		t.Fatalf("inbound event metadata: %v", err)
	}
	var metadata map[string]any
	if err := json.Unmarshal(raw, &metadata); err != nil {
		t.Fatalf("unmarshal metadata: %v", err)
	}
	files, ok := metadata["files"].([]any)
	if !ok || len(files) != 2 {
		t.Fatalf("metadata files = %#v, want two files", metadata["files"])
	}
	first, ok := files[0].(map[string]any)
	if !ok {
		t.Fatalf("first file metadata = %#v", files[0])
	}
	if first["ordinal"] != float64(0) || first["id"] != "F123" || first["status"] != EventFileStatusStored {
		t.Fatalf("first file metadata = %#v", first)
	}
	if _, ok := first["content"]; ok {
		t.Fatalf("first file metadata serialized content: %#v", first)
	}
	second, ok := files[1].(map[string]any)
	if !ok {
		t.Fatalf("second file metadata = %#v", files[1])
	}
	if _, ok := second["ordinal"]; ok {
		t.Fatalf("second file metadata serialized ordinal: %#v", second)
	}
	if second["reason"] != "too_many_attachments" || second["count"] != float64(2) {
		t.Fatalf("second file metadata = %#v", second)
	}
}

func TestRenderTextRendersSlackBroadcastMentions(t *testing.T) {
	labels := DisplayLabels{}
	got := labels.RenderText("<!here> <!channel> <!everyone> <!here|here> <!subteam^S123|team> <!date^123^{date}|jan>")
	want := "@here @channel @everyone @here <!subteam^S123|team> <!date^123^{date}|jan>"
	if got != want {
		t.Fatalf("RenderText = %q, want %q", got, want)
	}
}

func TestEnvelopeKinds(t *testing.T) {
	challenge, ok := URLVerificationChallenge(EventsEnvelope{Type: "url_verification", Challenge: "abc"})
	if !ok || challenge != "abc" {
		t.Fatalf("URLVerificationChallenge = %q, %v; want abc, true", challenge, ok)
	}
	if EventCallbackEnvelope(EventsEnvelope{Type: "url_verification"}) {
		t.Fatal("EventCallbackEnvelope for url_verification = true, want false")
	}
	if !EventCallbackEnvelope(EventsEnvelope{Type: "event_callback"}) {
		t.Fatal("EventCallbackEnvelope for event_callback = false, want true")
	}
}

func TestInstallLifecycleEvents(t *testing.T) {
	if !DisabledInstallEvent("U_BOT", Event{Type: "app_uninstalled"}) {
		t.Fatal("DisabledInstallEvent for app_uninstalled = false, want true")
	}
	if !DisabledInstallEvent("U_BOT", Event{Type: "tokens_revoked", Tokens: RevokedTokens{Bot: []string{"U_BOT"}}}) {
		t.Fatal("DisabledInstallEvent for revoked bot token = false, want true")
	}
	if DisabledInstallEvent("U_BOT", Event{Type: "tokens_revoked", Tokens: RevokedTokens{Bot: []string{"U_OTHER"}}}) {
		t.Fatal("DisabledInstallEvent for other revoked bot token = true, want false")
	}
	if IgnoredLifecycleEvent("U_BOT", Event{Type: "tokens_revoked", Tokens: RevokedTokens{Bot: []string{"U_BOT"}}}) {
		t.Fatal("IgnoredLifecycleEvent for revoked bot token = true, want false")
	}
	if !IgnoredLifecycleEvent("U_BOT", Event{Type: "tokens_revoked", Tokens: RevokedTokens{Bot: []string{"U_OTHER"}}}) {
		t.Fatal("IgnoredLifecycleEvent for other revoked bot token = false, want true")
	}
}

func TestDecodeEventsEnvelopeNameUpdates(t *testing.T) {
	raw := []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev1","event":{"type":"user_profile_changed","user":{"id":"U123",` +
		`"name":"ada","profile":{"display_name":"Ada Lovelace"}}}}`)
	envelope, err := DecodeEventsEnvelope(raw)
	if err != nil {
		t.Fatalf("decode user_profile_changed envelope: %v", err)
	}
	if envelope.Event.Type != "user_profile_changed" {
		t.Fatalf("event type = %q, want user_profile_changed", envelope.Event.Type)
	}
	update, ok := EventNameUpdate(envelope)
	if !ok || update.UserID != "U123" || update.DisplayName != "Ada Lovelace" ||
		update.ConversationID != "" {
		t.Fatalf("user_profile_changed update = %+v ok=%v", update, ok)
	}

	raw = []byte(`{"type":"event_callback","team_id":"T123","api_app_id":"A123",` +
		`"event_id":"Ev2","event":{"type":"channel_rename",` +
		`"channel":{"id":"C123","name":"general-renamed"}}}`)
	envelope, err = DecodeEventsEnvelope(raw)
	if err != nil {
		t.Fatalf("decode channel_rename envelope: %v", err)
	}
	update, ok = EventNameUpdate(envelope)
	if !ok || update.ConversationID != "C123" || update.DisplayName != "general-renamed" ||
		update.UserID != "" {
		t.Fatalf("channel_rename update = %+v ok=%v", update, ok)
	}

	if _, ok := EventNameUpdate(EventsEnvelope{Event: Event{Type: "message"}}); ok {
		t.Fatal("message event should not be a name update")
	}
}

type displayLabelLookupRecorder struct {
	mu       sync.Mutex
	users    []string
	channels []string
}

func (r *displayLabelLookupRecorder) lookups() ([]string, []string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.users...), append([]string(nil), r.channels...)
}

func newDisplayLabelLookupConfig(
	t *testing.T,
	userNames, channelNames map[string]string,
) (OAuthConfig, *displayLabelLookupRecorder) {
	t.Helper()
	recorder := &displayLabelLookupRecorder{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/users.info":
			userID := r.Form.Get("user")
			recorder.mu.Lock()
			recorder.users = append(recorder.users, userID)
			recorder.mu.Unlock()
			name := userNames[userID]
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok": true,
				"user": map[string]any{
					"name":      name,
					"real_name": name,
					"profile":   map[string]string{"display_name": name},
				},
			})
		case "/conversations.info":
			channelID := r.Form.Get("channel")
			recorder.mu.Lock()
			recorder.channels = append(recorder.channels, channelID)
			recorder.mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{
				"ok":      true,
				"channel": map[string]string{"name": channelNames[channelID]},
			})
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}, recorder
}

func TestResolveDisplayLabelsUsesStoredNamesBeforeLookups(t *testing.T) {
	config, recorder := newDisplayLabelLookupConfig(
		t,
		map[string]string{"U999": "Cal"},
		map[string]string{"C999": "eng"},
	)
	input := DisplayLabelInput{
		Event: Event{
			User:        "U123",
			Text:        "<@U_BOT> ask <@U456> and <@U999> in <#C999>",
			Channel:     "C123",
			ChannelType: "channel",
		},
		HistoryMessages: []HistoryMessage{
			{User: "U789", Text: "earlier"},
		},
		BotUserID:              "U_BOT",
		BotDisplayName:         "Omnara",
		ChannelDisplayName:     "general",
		StoredUserDisplayNames: map[string]string{"U123": "Ada", "U456": "Ben", "U789": "Grace"},
	}
	labels, resolvedChannels, err := ResolveDisplayLabels(
		context.Background(),
		config,
		"xoxb-test",
		input,
	)
	if err != nil {
		t.Fatalf("ResolveDisplayLabels: %v", err)
	}
	got := labels.RenderText("<@U_BOT> ask <@U456> and <@U999> in <#C123> then <#C999>")
	want := "<@U_BOT> (Omnara) ask <@U456> (Ben) and <@U999> (Cal) " +
		"in <#C123> (#general) then <#C999> (#eng)"
	if got != want {
		t.Fatalf("RenderText = %q", got)
	}
	if got := ReferencedUserIDs(input.Event, input.HistoryMessages); !reflect.DeepEqual(
		got,
		[]string{"U123", "U_BOT", "U456", "U999", "U789"},
	) {
		t.Fatalf("ReferencedUserIDs = %v", got)
	}
	userLookups, channelLookups := recorder.lookups()
	if !reflect.DeepEqual(userLookups, []string{"U999"}) {
		t.Fatalf("userLookups = %v, want [U999]", userLookups)
	}
	if !reflect.DeepEqual(channelLookups, []string{"C999"}) {
		t.Fatalf("channelLookups = %v, want [C999]", channelLookups)
	}
	if !reflect.DeepEqual(resolvedChannels, map[string]string{"C999": "eng"}) {
		t.Fatalf("resolvedChannels = %v", resolvedChannels)
	}
}

func TestResolveDisplayLabelsLiveResolvesHistoryAuthors(t *testing.T) {
	config, _ := newDisplayLabelLookupConfig(t, map[string]string{"U999": "Grace"}, nil)
	labels, _, err := ResolveDisplayLabels(
		context.Background(),
		config,
		"xoxb-test",
		DisplayLabelInput{
			Event: Event{
				User:        "U123",
				Text:        "<@U_BOT> use the prior context",
				Channel:     "C123",
				ChannelType: "channel",
			},
			HistoryMessages: []HistoryMessage{{User: "U999", Text: "earlier"}},
			BotUserID:       "U_BOT",
			BotDisplayName:  "Omnara",
		},
	)
	if err != nil {
		t.Fatalf("ResolveDisplayLabels: %v", err)
	}
	if got := labels.UserReference("U999"); got != "<@U999> (Grace)" {
		t.Fatalf("history author reference = %q, want <@U999> (Grace)", got)
	}
}

func TestResolveDisplayLabelsResolvesHistoryMentionsFromStoredNames(t *testing.T) {
	config, recorder := newDisplayLabelLookupConfig(
		t,
		nil,
		map[string]string{"C123": "C123", "C_HISTORY": "eng"},
	)
	labels, resolvedChannels, err := ResolveDisplayLabels(
		context.Background(),
		config,
		"xoxb-test",
		DisplayLabelInput{
			Event: Event{
				User:        "U123",
				Text:        "<@U_BOT> run",
				Channel:     "C123",
				ChannelType: "channel",
			},
			HistoryMessages: []HistoryMessage{
				{
					User: "U789",
					Text: "ask <@U_HISTORY> about <#C_HISTORY>",
				},
			},
			BotUserID:              "U_BOT",
			BotDisplayName:         "Omnara",
			StoredUserDisplayNames: map[string]string{"U123": "Ada", "U789": "Grace", "U_HISTORY": "Historia"},
		},
	)
	if err != nil {
		t.Fatalf("ResolveDisplayLabels: %v", err)
	}
	got := labels.RenderText("ask <@U_HISTORY> about <#C_HISTORY>")
	if got != "ask <@U_HISTORY> (Historia) about <#C_HISTORY> (#eng)" {
		t.Fatalf("RenderText = %q", got)
	}
	userLookups, channelLookups := recorder.lookups()
	if len(userLookups) != 0 {
		t.Fatalf("userLookups = %v, want empty", userLookups)
	}
	sort.Strings(channelLookups)
	if !reflect.DeepEqual(channelLookups, []string{"C123", "C_HISTORY"}) {
		t.Fatalf("channelLookups = %v, want [C123 C_HISTORY]", channelLookups)
	}
	if !reflect.DeepEqual(resolvedChannels, map[string]string{"C123": "C123", "C_HISTORY": "eng"}) {
		t.Fatalf("resolvedChannels = %v", resolvedChannels)
	}
}

func TestResolveDisplayLabelsCapsLiveLookups(t *testing.T) {
	config, recorder := newDisplayLabelLookupConfig(
		t,
		map[string]string{"U1": "U1", "U2": "U2", "U3": "U3"},
		map[string]string{"C0": "C0", "C1": "C1", "C2": "C2", "C3": "C3"},
	)
	_, _, err := ResolveDisplayLabels(
		context.Background(),
		config,
		"xoxb-test",
		DisplayLabelInput{
			Event: Event{
				User:        "U123",
				Text:        "<@U1> <@U2> <@U3> <#C1> <#C2> <#C3>",
				Channel:     "C0",
				ChannelType: "channel",
			},
			LiveUserLookupLimit:    2,
			LiveChannelLookupLimit: 2,
			StoredUserDisplayNames: map[string]string{"U123": "Ada"},
		},
	)
	if err != nil {
		t.Fatalf("ResolveDisplayLabels: %v", err)
	}
	userLookups, channelLookups := recorder.lookups()
	sort.Strings(userLookups)
	sort.Strings(channelLookups)
	if !reflect.DeepEqual(userLookups, []string{"U1", "U2"}) {
		t.Fatalf("userLookups = %v, want [U1 U2]", userLookups)
	}
	if !reflect.DeepEqual(channelLookups, []string{"C0", "C1"}) {
		t.Fatalf("channelLookups = %v, want [C0 C1]", channelLookups)
	}
}

func TestResolveDisplayLabelsContainsLookupPanic(t *testing.T) {
	_, _, err := ResolveDisplayLabels(
		context.Background(),
		OAuthConfig{
			APIURL:     "https://example.com",
			HTTPClient: &http.Client{Transport: panicRoundTripper{}},
		},
		"xoxb-test",
		DisplayLabelInput{
			Event: Event{
				User:        "U123",
				Channel:     "C123",
				ChannelType: "channel",
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), `lookup slack display name for "U123" panicked`) {
		t.Fatalf("ResolveDisplayLabels error = %v, want contained lookup panic", err)
	}
}

func TestResolveDisplayLabelsUsesStoredNamesForAllReferencedUsers(t *testing.T) {
	config, recorder := newDisplayLabelLookupConfig(t, nil, nil)
	input := DisplayLabelInput{
		Event: Event{
			User:        "U123",
			Text:        "<@U1> <@U2>",
			Channel:     "C123",
			ChannelType: "channel",
		},
		HistoryMessages: []HistoryMessage{
			{User: "U4", Text: "earlier"},
			{User: "U5", Text: "earlier"},
		},
		StoredUserDisplayNames: map[string]string{"U123": "Ada", "U1": "One", "U4": "Four"},
	}
	_, _, err := ResolveDisplayLabels(
		context.Background(),
		config,
		"xoxb-test",
		input,
	)
	if err != nil {
		t.Fatalf("ResolveDisplayLabels: %v", err)
	}
	if got := ReferencedUserIDs(input.Event, input.HistoryMessages); !reflect.DeepEqual(
		got,
		[]string{"U123", "U1", "U2", "U4", "U5"},
	) {
		t.Fatalf("ReferencedUserIDs = %v", got)
	}
	liveLookups, _ := recorder.lookups()
	sort.Strings(liveLookups)
	if !reflect.DeepEqual(liveLookups, []string{"U2", "U5"}) {
		t.Fatalf("liveLookups = %v, want stored names to suppress live lookups", liveLookups)
	}
}

func TestStoredDisplayNameUsesResolvedLabel(t *testing.T) {
	labels := DisplayLabels{Users: map[string]string{"U123": "Ada"}}
	if got := labels.StoredDisplayName("U123"); got != "Ada" {
		t.Fatalf("StoredDisplayName = %q, want Ada", got)
	}
}

func TestStoredDisplayNameDoesNotStoreRawMentionFallback(t *testing.T) {
	if got := (DisplayLabels{}).StoredDisplayName("U123"); got != "" {
		t.Fatalf("StoredDisplayName unresolved = %q, want empty", got)
	}
	poisoned := DisplayLabels{Users: map[string]string{"U123": "<@U123>"}}
	if got := poisoned.StoredDisplayName("U123"); got != "" {
		t.Fatalf("StoredDisplayName raw fallback = %q, want empty", got)
	}
}
