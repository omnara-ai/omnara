package slack

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAddReactionTreatsAlreadyReactedAsSuccess(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/reactions.add" {
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false,"error":"already_reacted"}`))
	}))
	defer server.Close()

	result, err := AddReaction(
		context.Background(),
		OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()},
		"xoxb-test",
		"C123",
		"111.222",
		"eyes",
	)
	if err != nil {
		t.Fatalf("add reaction: %v", err)
	}
	if result != (APIResult{}) {
		t.Fatalf("result = %+v, want success", result)
	}
}
