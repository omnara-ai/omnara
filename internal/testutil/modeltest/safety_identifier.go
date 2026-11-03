package modeltest

import (
	"encoding/json"
	"testing"
)

const (
	// SafetyIdentifierKey is a non-secret, base64-encoded 32-byte key for test workers.
	SafetyIdentifierKey = "MDEyMzQ1Njc4OWFiY2RlZjAxMjM0NTY3ODlhYmNkZWY="
	// LiveSafetyIdentifier stands in for the executor's org hash in adapter-only tests.
	LiveSafetyIdentifier = "omnara-live-provider-tests"
)

// RequireSafetyIdentifier checks the actual request sent by a live adapter test.
// The adapter unit tests separately pin each provider's field name and host gating.
func RequireSafetyIdentifier(t testing.TB, body json.RawMessage) {
	t.Helper()
	var request struct {
		User             string `json:"user"`
		SafetyIdentifier string `json:"safety_identifier"`
		Metadata         struct {
			UserID string `json:"user_id"`
		} `json:"metadata"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatalf("decode live request: %v", err)
	}
	if request.User != LiveSafetyIdentifier && request.SafetyIdentifier != LiveSafetyIdentifier &&
		request.Metadata.UserID != LiveSafetyIdentifier {
		t.Fatal("live provider request is missing the test safety identifier; " +
			"check that the base URL uses a supported provider host")
	}
}
