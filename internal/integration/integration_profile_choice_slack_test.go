package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type slackChoiceTransport func(*http.Request) (*http.Response, error)

func (f slackChoiceTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestSlackProfileChoiceSkipsOldHistoryAndRecoversAtInclusiveBoundary(t *testing.T) {
	type historyMessage struct {
		User     string           `json:"user"`
		TS       string           `json:"ts"`
		ThreadTS string           `json:"thread_ts,omitempty"`
		Blocks   []map[string]any `json:"blocks,omitempty"`
	}
	for _, kind := range []string{"channel", "thread"} {
		t.Run(kind, func(t *testing.T) {
			setup := slackInboxTestIntegration()
			access := &slackInboxTestAccess{integrationSetup: setup, version: uuid.New()}
			choice := providerProfileChoice(t)
			choice.CreatedAt = time.Unix(1700000300, 123456789)
			choice.Address = integrationstore.ConversationAddress{Kind: kind, Ref: "C123"}
			method := "/conversations.history"
			if kind == "thread" {
				choice.Address.Ref += ":1699900000.000000"
				method = "/conversations.replies"
			}
			oldMessages := make([]historyMessage, 900)
			for i := range oldMessages {
				oldMessages[i] = historyMessage{User: "UOLD", TS: fmt.Sprintf("%d.000000", 1699900000+i)}
			}
			var published atomic.Pointer[historyMessage]
			var reads, posts, historicalMessages atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth.test":
					_, _ = fmt.Fprint(w, `{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`)
				case method:
					reads.Add(1)
					assert.NoError(t, r.ParseForm())
					assert.Equal(t, "1700000000.123456", r.Form.Get("oldest"))
					assert.Equal(t, "true", r.Form.Get("inclusive"))
					oldest := new(big.Rat)
					if raw := r.Form.Get("oldest"); raw != "" {
						if _, ok := oldest.SetString(raw); !assert.True(t, ok, "invalid Slack timestamp %q", raw) {
							w.WriteHeader(http.StatusBadRequest)
							return
						}
					}
					messages := make([]historyMessage, 0, len(oldMessages)+1)
					candidates := append([]historyMessage{}, oldMessages...)
					if menu := published.Load(); menu != nil {
						candidates = append(candidates, *menu)
					}
					for _, message := range candidates {
						timestamp, ok := new(big.Rat).SetString(message.TS)
						if !assert.True(t, ok) {
							return
						}
						comparison := timestamp.Cmp(oldest)
						if comparison > 0 || (comparison == 0 && r.Form.Get("inclusive") == "true") {
							messages = append(messages, message)
						}
					}
					start := 0
					if cursor := r.Form.Get("cursor"); cursor != "" {
						var err error
						start, err = strconv.Atoi(cursor)
						assert.NoError(t, err)
					}
					end := min(start+100, len(messages))
					for _, message := range messages[start:end] {
						if message.User == "UOLD" {
							historicalMessages.Add(1)
						}
					}
					next := ""
					if end < len(messages) {
						next = strconv.Itoa(end)
					}
					assert.NoError(t, json.NewEncoder(w).Encode(map[string]any{
						"ok": true, "messages": messages[start:end], "has_more": next != "",
						"response_metadata": map[string]string{"next_cursor": next},
					}))
				case "/chat.postMessage":
					posts.Add(1)
					var menu historyMessage
					assert.NoError(t, json.NewDecoder(r.Body).Decode(&menu))
					menu.User, menu.TS = "UBOT", "1700000000.123456"
					published.Store(&menu)
					_, _ = fmt.Fprint(w, `{"ok":true,"channel":"C123","ts":"1700000000.123456"}`)
				default:
					t.Errorf("unexpected provider method %s", r.URL.Path)
				}
			}))
			defer server.Close()
			config := slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}
			for attempt := range 2 {
				// A fresh provider receives the same persisted creation time, but no recorded message ID.
				provider := NewSlackIntegrationInboxProvider(config, access, access, nil)
				channel, message, err := provider.PresentProfileChoice(t.Context(), setup, choice, nil)
				require.NoError(t, err)
				require.Equal(t, "C123", channel)
				require.Equal(t, "1700000000.123456", message)
				require.EqualValues(t, 1, posts.Load())
				require.EqualValues(t, attempt+1, reads.Load())
				require.Zero(t, historicalMessages.Load(), "pre-choice history must not consume the page budget")
			}
		})
	}
}

func TestSlackProfileChoiceRecoversUnrecordedMenu(t *testing.T) {
	for _, scenario := range []string{
		"restart", "timeout after publication", "server error after publication", "unconfirmed publication",
	} {
		t.Run(scenario, func(t *testing.T) {
			setup := slackInboxTestIntegration()
			access := &slackInboxTestAccess{integrationSetup: setup, version: uuid.New()}
			choice := providerProfileChoice(t)
			choice.CreatedAt = time.Unix(301, 0)
			choice.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
			choiceID, err := publicid.Encode(publicid.KindIntegrationProfileChoice, choice.ID)
			require.NoError(t, err)
			var reads, posts, checks atomic.Int32
			var visible atomic.Bool
			visible.Store(scenario == "restart")
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth.test":
					_, _ = fmt.Fprint(w, `{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`)
				case "/conversations.replies":
					reads.Add(1)
					assert.NoError(t, r.ParseForm())
					assert.Equal(t, "C123", r.Form.Get("channel"))
					assert.Equal(t, "1.2", r.Form.Get("ts"))
					assert.Equal(t, "1.000000", r.Form.Get("oldest"))
					if visible.Load() {
						_, _ = fmt.Fprintf(w, `{
							"ok":true,
							"messages":[{
								"user":"UBOT","thread_ts":"1.2","ts":"2.3",
								"blocks":[{"elements":[{"type":"static_select","action_id":%q}]}]
							}]
						}`, slack.ProfileChoiceActionPrefix+choiceID)
					} else {
						_, _ = fmt.Fprint(w, `{"ok":true,"messages":[]}`)
					}
				case "/chat.postMessage":
					posts.Add(1)
					visible.Store(scenario != "unconfirmed publication")
					if scenario != "timeout after publication" {
						w.WriteHeader(http.StatusServiceUnavailable)
					}
					_, _ = fmt.Fprint(w, `{"ok":true,"channel":"C123","ts":"2.3"}`)
				default:
					t.Errorf("unexpected provider method %s", r.URL.Path)
				}
			}))
			defer server.Close()
			client := server.Client()
			base := client.Transport
			client.Transport = slackChoiceTransport(func(r *http.Request) (*http.Response, error) {
				response, err := base.RoundTrip(r)
				if err == nil && scenario == "timeout after publication" && r.URL.Path == "/chat.postMessage" {
					_ = response.Body.Close()
					return nil, context.DeadlineExceeded
				}
				return response, err
			})
			config := slack.OAuthConfig{APIURL: server.URL, HTTPClient: client}
			check := func(context.Context) error { checks.Add(1); return nil }
			provider := NewSlackIntegrationInboxProvider(config, access, access, nil)
			channel, message, err := provider.PresentProfileChoice(t.Context(), setup, choice, check)
			if scenario == "unconfirmed publication" {
				var apiErr *slack.APIError
				require.ErrorAs(t, err, &apiErr)
				require.True(t, apiErr.Result.DeliveryUnknown)
				require.Empty(t, channel)
				require.Empty(t, message)
				visible.Store(true)
			} else {
				require.NoError(t, err)
				require.Equal(t, "C123", channel)
				require.Equal(t, "2.3", message)
			}
			wantPosts := int32(1)
			if scenario == "restart" {
				wantPosts = 0
			}
			require.Equal(t, wantPosts, posts.Load())
			// The publication receipt was never recorded; a new provider must recover it again.
			provider = NewSlackIntegrationInboxProvider(config, access, access, nil)
			channel, message, err = provider.PresentProfileChoice(t.Context(), setup, choice, check)
			require.NoError(t, err)
			require.Equal(t, "C123", channel)
			require.Equal(t, "2.3", message)
			require.Equal(t, wantPosts, posts.Load(), "recovery must not publish another menu")
			require.GreaterOrEqual(t, checks.Load(), reads.Load()+posts.Load())
		})
	}
}

func TestSlackProfileChoiceDoesNotPostAfterUncertainReadOrRevocation(t *testing.T) {
	for _, scenario := range []string{
		"rate limited", "unknown response", "incomplete page", "page limit",
		"revoked before first read", "revoked before next page", "revoked before post",
	} {
		t.Run(scenario, func(t *testing.T) {
			setup := slackInboxTestIntegration()
			access := &slackInboxTestAccess{integrationSetup: setup, version: uuid.New()}
			choice := providerProfileChoice(t)
			choice.Address = integrationstore.ConversationAddress{Kind: "thread", Ref: "C123:1.2"}
			var reads, posts atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/auth.test":
					_, _ = fmt.Fprint(w, `{"ok":true,"team_id":"T123","user_id":"UBOT","bot_id":"B123"}`)
				case "/conversations.replies":
					page := reads.Add(1)
					switch scenario {
					case "rate limited":
						w.Header().Set("Retry-After", "90")
						w.WriteHeader(http.StatusTooManyRequests)
					case "unknown response":
						_, _ = fmt.Fprint(w, `{`)
					case "incomplete page":
						_, _ = fmt.Fprint(w, `{"ok":true,"messages":[],"has_more":true}`)
					case "revoked before post":
						access.mu.Lock()
						access.revoked = true
						access.mu.Unlock()
						_, _ = fmt.Fprint(w, `{"ok":true,"messages":[]}`)
					default:
						_, _ = fmt.Fprintf(w, `{"ok":true,"messages":[],"response_metadata":{"next_cursor":"%d"}}`, page)
					}
				case "/chat.postMessage":
					posts.Add(1)
					t.Error("posted without confirmed absence and live authority")
				default:
					t.Errorf("unexpected provider method %s", r.URL.Path)
				}
			}))
			defer server.Close()
			config := slack.OAuthConfig{APIURL: server.URL, HTTPClient: server.Client()}
			provider := NewSlackIntegrationInboxProvider(config, access, access, nil)
			check := func(context.Context) error {
				if scenario == "revoked before first read" || (scenario == "revoked before next page" && reads.Load() > 0) {
					return storeerr.ErrUnauthorized
				}
				return nil
			}
			channel, message, err := provider.PresentProfileChoice(t.Context(), setup, choice, check)
			require.Error(t, err)
			require.Empty(t, channel)
			require.Empty(t, message)
			require.Zero(t, posts.Load())
			require.LessOrEqual(t, reads.Load(), int32(8))
			if scenario == "revoked before first read" {
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
				require.Zero(t, reads.Load(), "workflow authority must be checked before the first history request")
			} else if scenario == "revoked before next page" || scenario == "revoked before post" {
				require.ErrorIs(t, err, storeerr.ErrUnauthorized)
				require.EqualValues(t, 1, reads.Load())
			} else if scenario == "rate limited" {
				var apiErr *slack.APIError
				require.ErrorAs(t, err, &apiErr)
				require.True(t, apiErr.Result.RateLimited)
				require.Equal(t, 90*time.Second, apiErr.RetryDelay())
			}
		})
	}
}
