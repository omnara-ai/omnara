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

func TestSlackInboundRouting(t *testing.T) {
	botUserID := "U_BOT"
	tests := []struct {
		name           string
		event          slack.Event
		wantRef        string
		wantKind       string
		wantAppendOnly bool
		wantOK         bool
	}{
		{
			name: "root app mention creates thread ref",
			event: slack.Event{
				Type:        "app_mention",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "111.222",
				Text:        "<@U_BOT> run",
			},
			wantRef:  "C123:111.222",
			wantKind: "thread",
			wantOK:   true,
		},
		{
			name: "thread app mention routes to thread root",
			event: slack.Event{
				Type:        "app_mention",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "222.333",
				ThreadTS:    "111.222",
				Text:        "<@U_BOT> continue",
			},
			wantRef:  "C123:111.222",
			wantKind: "thread",
			wantOK:   true,
		},
		{
			name: "dm creates dm ref",
			event: slack.Event{
				Type:        "message",
				Channel:     "D123",
				ChannelType: "im",
				TS:          "111.222",
				Text:        "hello",
			},
			wantRef:  "D123",
			wantKind: "dm",
			wantOK:   true,
		},
		{
			name: "message subtype ignored",
			event: slack.Event{
				Type:        "message",
				Subtype:     "message_changed",
				Channel:     "D123",
				ChannelType: "im",
				TS:          "111.222",
				Text:        "edited",
			},
			wantOK: false,
		},
		{
			name: "dm file share creates dm ref",
			event: slack.Event{
				Type:        "message",
				Subtype:     "file_share",
				Channel:     "D123",
				ChannelType: "im",
				TS:          "111.222",
				Files:       []slack.File{{ID: "F123"}},
			},
			wantRef:  "D123",
			wantKind: "dm",
			wantOK:   true,
		},
		{
			name: "mentioned file share creates thread ref",
			event: slack.Event{
				Type:        "message",
				Subtype:     "file_share",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "111.222",
				Text:        "<@U_BOT> run",
				Files:       []slack.File{{ID: "F123"}},
			},
			wantRef:  "C123:111.222",
			wantKind: "thread",
			wantOK:   true,
		},
		{
			name: "root channel file share without mention appends existing thread ref",
			event: slack.Event{
				Type:        "message",
				Subtype:     "file_share",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "111.222",
				Files:       []slack.File{{ID: "F123"}},
			},
			wantRef:        "C123:111.222",
			wantKind:       "thread",
			wantAppendOnly: true,
			wantOK:         true,
		},
		{
			name: "thread reply appends mapped thread",
			event: slack.Event{
				Type:        "message",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "222.333",
				ThreadTS:    "111.222",
				Text:        "follow up",
			},
			wantRef:        "C123:111.222",
			wantKind:       "thread",
			wantAppendOnly: true,
			wantOK:         true,
		},
		{
			name: "mentioned text thread reply ignored for app mention dedupe",
			event: slack.Event{
				Type:        "message",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "222.333",
				ThreadTS:    "111.222",
				Text:        "<@U_BOT> follow up",
			},
			wantOK: false,
		},
		{
			name: "mentioned thread file share without files ignored for app mention dedupe",
			event: slack.Event{
				Type:        "message",
				Subtype:     "file_share",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "222.333",
				ThreadTS:    "111.222",
				Text:        "<@U_BOT> follow up",
			},
			wantOK: false,
		},
		{
			name: "unmentioned thread file share appends mapped thread",
			event: slack.Event{
				Type:        "message",
				Subtype:     "file_share",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "222.333",
				ThreadTS:    "111.222",
				Text:        "follow up",
				Files:       []slack.File{{ID: "F123"}},
			},
			wantRef:        "C123:111.222",
			wantKind:       "thread",
			wantAppendOnly: true,
			wantOK:         true,
		},
		{
			name: "mentioned thread file share can create thread ref",
			event: slack.Event{
				Type:        "message",
				Subtype:     "file_share",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "222.333",
				ThreadTS:    "111.222",
				Text:        "<@U_BOT> follow up",
				Files:       []slack.File{{ID: "F123"}},
			},
			wantRef:  "C123:111.222",
			wantKind: "thread",
			wantOK:   true,
		},
		{
			name: "mentioned message event ignored for app mention dedupe",
			event: slack.Event{
				Type:        "message",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "111.222",
				Text:        "<@U_BOT> run",
			},
			wantOK: false,
		},
		{
			name: "labeled bot mention message event ignored for app mention dedupe",
			event: slack.Event{
				Type:        "message",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "111.222",
				Text:        "<@U_BOT|Omnara> run",
			},
			wantOK: false,
		},
		{
			name: "root channel message ignored",
			event: slack.Event{
				Type:        "message",
				Channel:     "C123",
				ChannelType: "channel",
				TS:          "111.222",
				Text:        "not for bot",
			},
			wantOK: false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			route, ok := slack.InboundRouting(botUserID, test.event)
			if ok != test.wantOK {
				t.Fatalf("ok=%v want %v route=%+v", ok, test.wantOK, route)
			}
			if !ok {
				return
			}
			if route.ProviderRef != test.wantRef ||
				route.ProviderRefKind != test.wantKind ||
				route.AppendOnly != test.wantAppendOnly {
				t.Fatalf(
					"route=%+v want ref=%s kind=%s append_only=%v",
					route,
					test.wantRef,
					test.wantKind,
					test.wantAppendOnly,
				)
			}
		})
	}
}

func TestSlackShouldFetchRecentContext(t *testing.T) {
	tests := []struct {
		name        string
		route       slack.InboundRoute
		newlyMapped bool
		want        bool
	}{
		{
			name:        "new thread mapping fetches",
			route:       slack.InboundRoute{ProviderRefKind: "thread"},
			newlyMapped: true,
			want:        true,
		},
		{
			name:        "existing thread skips",
			route:       slack.InboundRoute{ProviderRefKind: "thread"},
			newlyMapped: false,
		},
		{
			name:        "new dm skips",
			route:       slack.InboundRoute{ProviderRefKind: "dm"},
			newlyMapped: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := slack.ShouldFetchRecentContext(test.route, test.newlyMapped); got != test.want {
				t.Fatalf("ShouldFetchRecentContext=%v want %v", got, test.want)
			}
		})
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
