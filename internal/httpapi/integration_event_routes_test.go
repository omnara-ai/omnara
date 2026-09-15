package httpapi

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/slack"
)

func TestSlackSignatureValidation(t *testing.T) {
	body := []byte(`{"type":"event_callback"}`)
	now := time.Unix(1_700_000_000, 0).UTC()
	request, err := http.NewRequest(http.MethodPost, "/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	for key, value := range slackSignedHeadersAt(string(body), "secret", now) {
		request.Header.Set(key, value)
	}
	if !slack.ValidSignature(request.Header, body, "secret", now) {
		t.Fatal("valid signature rejected")
	}
	if slack.ValidSignature(request.Header, body, "wrong-secret", now) {
		t.Fatal("signature accepted with wrong secret")
	}
	if slack.ValidSignature(request.Header, body, "secret", now.Add(10*time.Minute)) {
		t.Fatal("too-old request timestamp accepted")
	}
	if slack.ValidSignature(request.Header, body, "secret", now.Add(-10*time.Minute)) {
		t.Fatal("future request timestamp accepted")
	}
}

func TestSlackEventsURLVerificationDoesNotRequireInstall(t *testing.T) {
	server := &Server{}
	request := httptest.NewRequest(
		http.MethodPost,
		integrationEventsPath,
		strings.NewReader(
			`{"type":"url_verification","challenge":"challenge-123"}`,
		),
	)
	recorder := httptest.NewRecorder()
	server.integrationEventsRoute(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response map[string]string
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if response["challenge"] != "challenge-123" {
		t.Fatalf("response=%v", response)
	}
}

func TestSlackRuntimeBotAuthorizationRequiresVisibleInstall(t *testing.T) {
	identity := slack.Identity{
		AppID:       "A123",
		WorkspaceID: "T123",
		BotUserID:   "U_BOT",
	}
	base := slack.EventsEnvelope{
		Type:     "event_callback",
		TeamID:   "T123",
		APIAppID: "A123",
	}
	tests := []struct {
		name           string
		authorizations []slack.Authorization
		want           bool
	}{
		{
			name: "matching bot authorization",
			authorizations: []slack.Authorization{
				{TeamID: "T123", UserID: "U_BOT", IsBot: true},
			},
			want: true,
		},
		{name: "empty authorizations rejected", authorizations: nil},
		{
			name: "wrong workspace authorization rejected",
			authorizations: []slack.Authorization{
				{TeamID: "T999", UserID: "U_BOT", IsBot: true},
			},
		},
		{
			name: "wrong bot authorization rejected",
			authorizations: []slack.Authorization{
				{TeamID: "T123", UserID: "U_OTHER", IsBot: true},
			},
		},
		{
			name: "non bot authorization rejected",
			authorizations: []slack.Authorization{
				{TeamID: "T123", UserID: "U_BOT"},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			envelope := base
			envelope.Authorizations = test.authorizations
			if got := slack.ValidateRuntimeBotAuthorization(identity, envelope); got != test.want {
				t.Fatalf(
					"ValidateRuntimeBotAuthorization=%v want %v",
					got,
					test.want,
				)
			}
		})
	}
}

func slackSignedHeadersAt(body, signingSecret string, signedAt time.Time) map[string]string {
	timestamp := strconv.FormatInt(signedAt.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(signingSecret))
	_, _ = mac.Write([]byte("v0:" + timestamp + ":" + body))
	return map[string]string{
		slack.TimestampHeader: timestamp,
		slack.SignatureHeader: "v0=" + hex.EncodeToString(mac.Sum(nil)),
	}
}
