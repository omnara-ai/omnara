package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
)

func TestAddReactionEncodesEmojiAndRetriesSafely(t *testing.T) {
	for _, status := range []int{
		http.StatusNoContent, http.StatusServiceUnavailable, http.StatusForbidden, http.StatusTooManyRequests,
	} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			var attempts atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				attempt := attempts.Add(1)
				if r.Method != http.MethodPut ||
					r.URL.EscapedPath() != "/api/v10/channels/444/messages/666/reactions/%F0%9F%91%80/@me" ||
					r.Header.Get("Authorization") != "Bot test-token" {
					t.Errorf("incorrect reaction request: %s %s", r.Method, r.URL.EscapedPath())
				}
				if status == http.StatusServiceUnavailable && attempt > 1 {
					w.WriteHeader(http.StatusNoContent)
				} else {
					w.WriteHeader(status)
				}
			})
			err := client.AddReaction(t.Context(), "444", "666", "👀")
			wantAttempts := int32(1)
			switch status {
			case http.StatusServiceUnavailable:
				wantAttempts = 2
				fallthrough
			case http.StatusNoContent:
				if err != nil {
					t.Fatal(err)
				}
			case http.StatusForbidden:
				requireAPIError(t, err, PermanentFailure)
			case http.StatusTooManyRequests:
				wantAttempts = 3
				requireAPIError(t, err, RateLimited)
			}
			if attempts.Load() != wantAttempts {
				t.Fatalf("reaction attempts = %d, want %d", attempts.Load(), wantAttempts)
			}
		})
	}
}

func TestEnsureThreadCreatesReusesAndReconciles(t *testing.T) {
	for _, mode := range []string{"existing", "create", "uncertain", "concurrent"} {
		t.Run(mode, func(t *testing.T) {
			var created atomic.Bool
			created.Store(mode == "existing")
			var posts atomic.Int32
			client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v10/channels/444":
					fmt.Fprint(w, parentJSON)
				case "/api/v10/channels/444/messages/555":
					fmt.Fprint(w, `{"id":"555","channel_id":"444","author":{"id":"888"}}`)
				case "/api/v10/channels/555":
					if !created.Load() {
						w.WriteHeader(http.StatusNotFound)
						return
					}
					fmt.Fprint(w, threadJSON)
				case "/api/v10/channels/444/messages/555/threads":
					if r.Method != http.MethodPost {
						t.Error("thread creation used wrong method")
					}
					posts.Add(1)
					created.Store(true)
					switch mode {
					case "uncertain":
						w.WriteHeader(http.StatusBadGateway)
					case "concurrent":
						w.WriteHeader(http.StatusBadRequest)
						fmt.Fprint(w, `{"code":160004}`)
					default:
						fmt.Fprint(w, threadJSON)
					}
				default:
					t.Error("unexpected thread endpoint")
				}
			})
			parentScope := Scope{GuildID: "333", ChannelID: "444"}
			thread, err := client.EnsureThread(t.Context(), parentScope, "555", "Mention thread")
			if err != nil || thread.ID != "555" || thread.ParentID != "444" {
				t.Fatalf("thread=%+v, err=%v", thread, err)
			}
			if _, err := client.EnsureThread(t.Context(), parentScope, "555", "Same thread"); err != nil {
				t.Fatal(err)
			}
			wantPosts := int32(1)
			if mode == "existing" {
				wantPosts = 0
			}
			if posts.Load() != wantPosts {
				t.Fatal("created a second thread or retried uncertain create")
			}
			thread, err = client.EnsureThread(t.Context(), scope(), "999", "Mention inside thread")
			if err != nil || thread.ID != "555" || posts.Load() != wantPosts {
				t.Fatal("nested mention did not reuse thread")
			}
		})
	}
}

func TestMultipartUploadBoundedAndNonceProtected(t *testing.T) {
	client, _ := testClient(t, prepared(func(w http.ResponseWriter, r *http.Request) {
		mediaType, params, err := mime.ParseMediaType(r.Header.Get("Content-Type"))
		if err != nil || mediaType != "multipart/form-data" {
			t.Error("wrong upload content type")
			return
		}
		reader := multipart.NewReader(r.Body, params["boundary"])
		part, err := reader.NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		var payload messagePayload
		if part.FormName() != "payload_json" || json.NewDecoder(part).Decode(&payload) != nil ||
			!payload.EnforceNonce || payload.Nonce != "send_1" || len(payload.Attachments) != 1 ||
			payload.Attachments[0].ID != 0 {
			t.Error("missing upload nonce or attachment metadata")
		}
		part, err = reader.NextPart()
		if err != nil {
			t.Error(err)
			return
		}
		body, _ := io.ReadAll(part)
		if part.FormName() != "files[0]" || part.FileName() != "notes.txt" || string(body) != "file contents" {
			t.Error("invalid multipart file")
		}
		if _, err := reader.NextPart(); !errors.Is(err, io.EOF) {
			t.Error("unexpected file parts")
		}
		fmt.Fprint(w, messageJSON)
	}))
	_, err := client.CreateMessage(t.Context(), scope(), MessageArgs{Nonce: "send_1",
		Files: []Upload{{Filename: "notes.txt", Content: []byte("file contents")}}})
	if err != nil {
		t.Fatal(err)
	}
	for _, files := range [][]Upload{
		{{Filename: "../secret", Content: []byte("x")}},
		{{Filename: "large", Content: make([]byte, MaxFileBytes+1)}},
		make([]Upload, MaxFiles+1),
	} {
		if _, err := client.CreateMessage(t.Context(), scope(), MessageArgs{Nonce: "send_1", Files: files}); err == nil {
			t.Fatal("accepted invalid file upload")
		}
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestDownloadRefreshesScopedMetadataAndNeverSendsToken(t *testing.T) {
	var cdnCalls int
	client, server := testClient(t, prepared(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v10/channels/555/messages/666" {
			t.Error("attachment lookup escaped message")
		}
		fmt.Fprint(w,
			`{"id":"666","channel_id":"555","author":{"id":"888"},"attachments":[{"id":"777","filename":"a.txt","size":5,"url":"https://cdn.discordapp.com/attachments/555/777/a.txt?ex=abc&hm=signed"}]}`)
	}))
	client.http.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Host == strings.TrimPrefix(server.URL, "http://") {
			return http.DefaultTransport.RoundTrip(r)
		}
		if r.URL.Host != "cdn.discordapp.com" {
			t.Error("unexpected download origin")
		}
		cdnCalls++
		if r.Header.Get("Authorization") != "" || r.URL.Query().Get("hm") != "signed" {
			t.Error("unsafe CDN request")
		}
		return &http.Response{StatusCode: http.StatusOK, Header: http.Header{},
			Body: io.NopCloser(strings.NewReader("hello")), Request: r}, nil
	})
	file, err := client.DownloadAttachment(t.Context(), scope(), "666", "777")
	if err != nil || string(file.Content) != "hello" || cdnCalls != 1 {
		t.Fatalf("download=%+v, error=%v", file, err)
	}
	_, err = client.DownloadAttachment(t.Context(), scope(), "666", "999")
	requireAPIError(t, err, ScopeMismatch)
	if cdnCalls != 1 {
		t.Fatal("downloaded an attachment outside scoped message")
	}
	for _, raw := range []string{"https://evil.example/attachments/555/777/a.txt",
		"https://cdn.discordapp.com:444/attachments/555/777/a.txt", "https://cdn.discordapp.com/attachments/999/777/a.txt",
		"https://user@cdn.discordapp.com/attachments/555/777/a.txt", "http://cdn.discordapp.com/attachments/555/777/a.txt"} {
		if validAttachmentURL(raw, "555", "777") {
			t.Errorf("accepted unsafe CDN URL: %s", raw)
		}
	}
}

func TestEditDoesNotRetryUnknownDelivery(t *testing.T) {
	var mutations atomic.Int32
	client, _ := testClient(t, prepared(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Error("wrong edit method")
		}
		mutations.Add(1)
		w.WriteHeader(http.StatusBadGateway)
	}))
	_, err := client.EditMessage(t.Context(), scope(), "666", "Resolved", nil)
	requireAPIError(t, err, DeliveryUnknown)
	if mutations.Load() != 1 {
		t.Fatal("retried ambiguous edit")
	}
}

func TestNormalizeMessageUsesBotIdentityAndIgnoresEdits(t *testing.T) {
	dispatch := Dispatch{Type: "MESSAGE_CREATE", Data: json.RawMessage(`{"id":"666","guild_id":"333","channel_id":"444","type":0,"content":"hello <@111>","author":{"id":"888"},"mentions":[{"id":"111"}]}`)}
	event, ok, err := NormalizeMessage(dispatch, "222")
	if err != nil || !ok || event.MentionsBot {
		t.Fatal("application ID mistaken for bot mention")
	}
	if event.Message.ID != "666" || event.Message.ChannelID != "444" || event.Message.GuildID != "333" ||
		event.Message.Author.ID != "888" || event.Message.Content != "hello <@111>" {
		t.Fatalf("message fields were not preserved: %+v", event.Message)
	}
	event, ok, err = NormalizeMessage(dispatch, "111")
	if err != nil || !ok || !event.MentionsBot || event.Self || event.Automated {
		t.Fatalf("human mention of the bot was not decoded: event=%+v, err=%v", event, err)
	}
	_, ok, err = NormalizeMessage(Dispatch{Type: "MESSAGE_UPDATE"}, "222")
	if err != nil || ok {
		t.Fatal("edits treated as new messages")
	}
}

func TestCanceledRequestDoesNoIO(t *testing.T) {
	var requests atomic.Int32
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) { requests.Add(1) })
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	_, err := client.ListMessages(ctx, scope(), PageOptions{})
	if !errors.Is(err, context.Canceled) || requests.Load() != 0 {
		t.Fatal("canceled request attempted I/O")
	}
}
