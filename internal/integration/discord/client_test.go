package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"
)

func credentials() Credentials {
	return Credentials{ApplicationID: "111", BotUserID: "222", BotToken: "test-token"}
}
func scope() Scope { return Scope{GuildID: "333", ChannelID: "444", ThreadID: "555"} }

const threadJSON = `{"id":"555","guild_id":"333","parent_id":"444","type":11}`
const parentJSON = `{"id":"444","guild_id":"333","type":0}`
const messageJSON = `{"id":"666","channel_id":"555","author":{"id":"222","bot":true},"nonce":"send_1"}`

func testClient(t *testing.T, handler http.HandlerFunc) (*Client, *httptest.Server) {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	client, err := NewClient(Config{
		Credentials: credentials(), HTTPClient: server.Client(), APIURL: server.URL + "/api/v10",
	})
	if err != nil {
		t.Fatal(err)
	}
	return client, server
}

func prepared(handler http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/api/v10/channels/555" {
			fmt.Fprint(w, threadJSON)
			return
		}
		handler(w, r)
	}
}

func requireAPIError(t *testing.T, err error, code ErrorCode) *APIError {
	t.Helper()
	var result *APIError
	if !errors.As(err, &result) || result.Code != code {
		t.Fatalf("error = %v, want %s", err, code)
	}
	return result
}

func TestBoundedReadsAndScope(t *testing.T) {
	var requests atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != "Bot test-token" || r.Header.Get("User-Agent") == "" {
			t.Error("missing bot auth or user agent")
		}
		switch r.URL.Path {
		case "/api/v10/channels/555":
			fmt.Fprint(w, threadJSON)
		case "/api/v10/channels/555/messages":
			if r.URL.Query().Get("limit") != "2" || r.URL.Query().Get("before") != "900" {
				t.Error("wrong page")
			}
			fmt.Fprint(w,
				`[{"id":"800","channel_id":"555","author":{"id":"222"}},{"id":"700","channel_id":"555","author":{"id":"123"}}]`)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
	})
	page, err := client.ListMessages(t.Context(), scope(), PageOptions{Before: "900", Limit: 2})
	if err != nil || page.NextBefore != "700" || len(page.Messages) != 2 || requests.Load() != 2 {
		t.Fatalf("page = %+v, error = %v, requests = %d", page, err, requests.Load())
	}
	for _, bad := range []string{"../666", "1?token=secret", "01", "0", "+4", "18446744073709551616"} {
		if _, err := client.GetMessage(t.Context(), scope(), bad); err == nil {
			t.Error("accepted unsafe ID")
		}
		if _, err := client.ListMessages(t.Context(), scope(), PageOptions{Before: bad}); err == nil {
			t.Error("unsafe cursor")
		}
	}
	if requests.Load() != 2 {
		t.Fatal("invalid arguments attempted I/O")
	}
}

func TestScopeMismatchBlocksWrites(t *testing.T) {
	for _, body := range []string{
		`{"id":"555","guild_id":"999","parent_id":"444","type":11}`,
		`{"id":"555","guild_id":"333","parent_id":"999","type":11}`,
		`{"id":"555","guild_id":"333","type":0}`,
	} {
		var requests atomic.Int32
		client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
			requests.Add(1)
			if r.Method != http.MethodGet {
				t.Error("scope mismatch sent mutation")
			}
			fmt.Fprint(w, body)
		})
		_, err := client.CreateMessage(t.Context(), scope(), MessageArgs{Content: "hello", Nonce: "send_1"})
		requireAPIError(t, err, ScopeMismatch)
		if requests.Load() != 1 {
			t.Fatal("unexpected scope I/O")
		}
	}
}

func TestNonceRetryAfterUncertainSend(t *testing.T) {
	var attempts atomic.Int32
	var firstBody []byte
	client, _ := testClient(t, prepared(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v10/channels/555/messages" {
			t.Error("wrong send endpoint")
		}
		body, _ := io.ReadAll(r.Body)
		var payload messagePayload
		if json.Unmarshal(body, &payload) != nil || payload.Nonce != "send_1" || !payload.EnforceNonce ||
			payload.AllowedMentions.Parse == nil || len(payload.AllowedMentions.Parse) != 0 {
			t.Error("unsafe send payload")
		}
		if attempts.Add(1) == 1 {
			firstBody = body
			conn, _, err := http.NewResponseController(w).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			conn.Close()
			return
		}
		if string(firstBody) != string(body) {
			t.Error("retry changed nonce or payload")
		}
		fmt.Fprint(w, messageJSON)
	}))
	message, err := client.CreateMessage(t.Context(), scope(), MessageArgs{Content: "hello @everyone", Nonce: "send_1"})
	if err != nil || message.ID != "666" || attempts.Load() != 2 {
		t.Fatalf("message=%+v, error=%v", message, err)
	}
}

func TestErrorsRateLimitsAndRetryBounds(t *testing.T) {
	for _, test := range []struct {
		name     string
		status   int
		body     string
		mutation bool
		code     ErrorCode
		calls    int32
	}{
		{"read transient", 503, "private body", false, TransientFailure, 3},
		{"short read throttle", 429, `{"retry_after":0.001}`, false, RateLimited, 3},
		{"short nonce throttle", 429, `{"retry_after":0.001}`, true, RateLimited, 3},
		{"nonce send transient", 503, "private body", true, DeliveryUnknown, 3},
		{"read forbidden", 403, `{"code":50013,"message":"test-token"}`, false, PermanentFailure, 1},
		{"rate limited", 429, `{"retry_after":72.5,"global":true}`, true, RateLimited, 1},
		{"malformed send receipt", 200, "not JSON", true, DeliveryUnknown, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			var requests atomic.Int32
			client, _ := testClient(t, prepared(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				w.WriteHeader(test.status)
				fmt.Fprint(w, test.body)
			}))
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			var err error
			if test.mutation {
				_, err = client.CreateMessage(ctx, scope(), MessageArgs{Content: "hi", Nonce: "send_1"})
			} else {
				_, err = client.ListMessages(ctx, scope(), PageOptions{})
			}
			apiErr := requireAPIError(t, err, test.code)
			if test.name == "rate limited" && (apiErr.RetryDelay() != 72500*time.Millisecond || !apiErr.Global) {
				t.Fatalf("lost rate limit facts: %+v", apiErr)
			}
			if ctx.Err() != nil || requests.Load() != test.calls || strings.Contains(err.Error(), "test-token") ||
				strings.Contains(err.Error(), "private") {
				t.Fatalf("error=%v, calls=%d", err, requests.Load())
			}
		})
	}
}

func TestUncertainSendThenDefiniteFailureStaysUncertain(t *testing.T) {
	var requests atomic.Int32
	client, _ := testClient(t, prepared(func(w http.ResponseWriter, r *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"retry_after":60}`)
	}))
	_, err := client.CreateMessage(t.Context(), scope(), MessageArgs{Content: "hi", Nonce: "send_1"})
	apiErr := requireAPIError(t, err, DeliveryUnknown)
	if apiErr.RetryAfter != time.Minute || requests.Load() != 2 {
		t.Fatal("lost prior delivery uncertainty")
	}
}

func TestBeforeRequestAndSharedDeadline(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		fmt.Fprint(w, threadJSON)
	}))
	defer server.Close()
	var deadline time.Time
	var hooks int
	client, err := NewClient(Config{
		Credentials: credentials(), HTTPClient: server.Client(), APIURL: server.URL + "/api/v10",
		BeforeRequest: func(ctx context.Context) error {
			hooks++
			got, ok := ctx.Deadline()
			if !ok || time.Until(got) > OperationTimeout {
				t.Error("unbounded hook context")
			}
			if hooks == 1 {
				deadline = got
				return nil
			}
			if deadline != got {
				t.Error("budget reset after preflight")
			}
			return context.Canceled
		}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.CreateMessage(t.Context(), scope(), MessageArgs{Content: "hi", Nonce: "send_1"})
	if err != context.Canceled || hooks != 2 || requests.Load() != 1 { //nolint:errorlint // Require unchanged hook error.
		t.Fatalf("error=%v, hooks=%d, requests=%d", err, hooks, requests.Load())
	}
}

func TestRedirectsAndResponseBounds(t *testing.T) {
	var escaped atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { escaped.Add(1) }))
	defer target.Close()
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/steal", http.StatusTemporaryRedirect)
	})
	_, err := client.GetChannel(t.Context(), "444")
	requireAPIError(t, err, PermanentFailure)
	if escaped.Load() != 0 {
		t.Fatal("forwarded bot token through redirect")
	}
	client, _ = testClient(t, func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, strings.Repeat("x", ResponseMaxBytes+1))
	})
	_, err = client.GetChannel(t.Context(), "444")
	requireAPIError(t, err, InvalidResponse)
}

func TestClientConfigAndIdentity(t *testing.T) {
	for _, raw := range []string{"http://discord.com/api/v10", "https://u:p@discord.com/api/v10",
		"https://discord.com/api/v10?q=x", "https://discord.com/api/v9"} {
		if _, err := NewClient(Config{Credentials: credentials(), APIURL: raw}); err == nil {
			t.Error("unsafe API URL")
		}
	}
	var appID = "111"
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v10/users/@me":
			fmt.Fprint(w, `{"id":"222","bot":true}`)
		case "/api/v10/applications/@me":
			fmt.Fprintf(w, `{"id":%q}`, appID)
		default:
			t.Error("wrong identity endpoint")
		}
	})
	if err := client.CheckIdentity(t.Context()); err != nil {
		t.Fatal(err)
	}
	appID = "333"
	requireAPIError(t, client.CheckIdentity(t.Context()), ScopeMismatch)
}

func TestDiscoverIdentityFromCustomerBotToken(t *testing.T) {
	globalName := "Helper"
	_, server := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v10/users/@me":
			fmt.Fprintf(w, `{"id":"222","bot":true,"username":"helper","global_name":%q}`, globalName)
		case "/api/v10/applications/@me":
			fmt.Fprint(w, `{"id":"111"}`)
		default:
			t.Error("unexpected identity request")
		}
	})
	config := Config{Credentials: Credentials{BotToken: "test-token"},
		HTTPClient: server.Client(), APIURL: server.URL + "/api/v10"}
	identity, err := DiscoverIdentity(t.Context(), config)
	if err != nil || identity.ApplicationID != "111" || identity.BotUserID != "222" || identity.DisplayName != "Helper" {
		t.Fatalf("identity=%+v, error=%v", identity, err)
	}
	globalName = ""
	identity, err = DiscoverIdentity(t.Context(), config)
	if err != nil || identity.DisplayName != "helper" {
		t.Fatalf("identity=%+v, error=%v", identity, err)
	}
	config.Credentials.ApplicationID = "333"
	_, err = DiscoverIdentity(t.Context(), config)
	requireAPIError(t, err, ScopeMismatch)
	config.Credentials.BotToken = "invalid token"
	_, err = DiscoverIdentity(t.Context(), config)
	requireAPIError(t, err, PermanentFailure)
}

func TestShortRateLimitRecoversWithoutChangingSend(t *testing.T) {
	for _, mutation := range []bool{false, true} {
		t.Run(fmt.Sprint(mutation), func(t *testing.T) {
			var calls atomic.Int32
			var first []byte
			client, _ := testClient(t, prepared(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				if calls.Add(1) == 1 {
					first = body
					w.WriteHeader(http.StatusTooManyRequests)
					fmt.Fprint(w, `{"retry_after":0.001}`)
					return
				}
				if string(first) != string(body) {
					t.Error("retry changed request body")
				}
				if mutation {
					fmt.Fprint(w, messageJSON)
				} else {
					fmt.Fprint(w, `[]`)
				}
			}))
			var err error
			if mutation {
				_, err = client.CreateMessage(t.Context(), scope(), MessageArgs{Content: "hi", Nonce: "send_1"})
			} else {
				_, err = client.ListMessages(t.Context(), scope(), PageOptions{})
			}
			if err != nil || calls.Load() != 2 {
				t.Fatalf("calls=%d error=%v", calls.Load(), err)
			}
		})
	}
}

func TestShortRateLimitRechecksAuthority(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusTooManyRequests)
		fmt.Fprint(w, `{"retry_after":0.001}`)
	})
	denied := errors.New("authority revoked")
	client.beforeRequest = func(context.Context) error {
		if calls.Load() > 0 {
			return denied
		}
		return nil
	}
	_, err := client.GetChannel(t.Context(), "555")
	if !errors.Is(err, denied) || calls.Load() != 1 {
		t.Fatalf("calls=%d error=%v", calls.Load(), err)
	}
}

func TestMessageLengthErrorBeforeNetwork(t *testing.T) {
	var calls atomic.Int32
	client, _ := testClient(t, prepared(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		fmt.Fprint(w, messageJSON)
	}))
	_, err := client.CreateMessage(t.Context(), scope(), MessageArgs{Content: strings.Repeat("🙂", 2001), Nonce: "send_1"})
	if err == nil || !strings.Contains(err.Error(), "2000 characters") || calls.Load() != 0 {
		t.Fatalf("calls=%d error=%v", calls.Load(), err)
	}
	_, err = client.CreateMessage(t.Context(), scope(), MessageArgs{Content: strings.Repeat("🙂", 2000), Nonce: "send_1"})
	if err != nil || calls.Load() != 1 {
		t.Fatalf("calls=%d error=%v", calls.Load(), err)
	}
}

func TestShortRateLimitWaitHonorsCancellation(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		calls := 0
		client, err := NewClient(Config{Credentials: credentials(), HTTPClient: &http.Client{
			Transport: roundTripFunc(func(r *http.Request) (*http.Response, error) {
				calls++
				return &http.Response{StatusCode: http.StatusTooManyRequests, Header: http.Header{},
					Body: io.NopCloser(strings.NewReader(`{"retry_after":1}`)), Request: r}, nil
			}),
		}})
		if err != nil {
			t.Fatal(err)
		}
		ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
		defer cancel()
		_, err = client.GetChannel(ctx, "555")
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
			t.Fatalf("calls=%d error=%v", calls, err)
		}
	})
}
