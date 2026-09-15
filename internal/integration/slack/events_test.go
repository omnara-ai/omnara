package slack

import "testing"

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
