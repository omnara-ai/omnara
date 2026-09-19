package github

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"strings"
	"testing"
)

func webhookHeaders(body []byte) http.Header {
	mac := hmac.New(sha256.New, []byte("test-secret"))
	_, _ = mac.Write(body)
	header := http.Header{}
	header.Set(SignatureHeader, "sha256="+hex.EncodeToString(mac.Sum(nil)))
	header.Set(EventHeader, "pull_request_review_comment")
	header.Set(DeliveryHeader, "12345678-1234-1234-1234-123456789abc")
	return header
}

func TestWebhookSignatureKnownVector(t *testing.T) {
	// RFC 4231 test case 1, with GitHub's sha256= header encoding. This fixed
	// vector is independent of the local fixture signing helper.
	secret := strings.Repeat("\x0b", 20)
	body := []byte("Hi There")
	header := http.Header{}
	header.Set(SignatureHeader, "sha256=b0344c61d8db38535ca8afceaf0bf12b881dc200c9833da726e9376c2e32cff7")
	if !ValidSignature(header, body, secret) {
		t.Fatal("known HMAC fixture did not verify")
	}
	if ValidSignature(header, []byte("Hello, world!"), secret) || ValidSignature(header, body, "") {
		t.Fatal("accepted modified bytes or missing secret")
	}
}

func TestWebhookExtractionPreservesActorAndAction(t *testing.T) {
	body := []byte(`{
		"action":"created","sender":{"id":90,"login":"customer-app[bot]","type":"Bot"},
		"installation":{"id":456,"app_id":123},
		"repository":{"id":1,"name":"repo","owner":{"login":"org","id":2}},
		"pull_request":{"id":6,"number":42,"head":{"sha":"new-commit"}},
		"comment":{"id":7,"body":"hello 🐙","user":{"id":91,"login":"author","type":"User"},
		"in_reply_to_id":5,"line":3,"side":"RIGHT","path":"file.go"},"before":"old","after":"new"
	}`)
	header := webhookHeaders(body)
	header.Set("X-Github-Hook-Id", "1234")
	header.Set("X-Github-Hook-Installation-Target-Id", "123")
	header.Set("X-Github-Hook-Installation-Target-Type", "integration")
	event, err := DecodeWebhook(header, body, "test-secret")
	if err != nil {
		t.Fatal(err)
	}
	if event.EventType != "pull_request_review_comment" || event.Action != "created" ||
		event.DeliveryID != header.Get(DeliveryHeader) || event.HookID != 1234 ||
		event.InstallationTargetID != 123 || event.InstallationTargetType != "integration" ||
		event.Installation.ID != 456 || event.PullRequest.Number != 42 || event.Repository.Owner.Login != "org" ||
		event.Sender.Type != "Bot" || event.Sender.ID != 90 || event.Comment.User.ID != 91 ||
		event.Comment.InReplyToID != 5 || event.Comment.Body != "hello 🐙" || event.After != "new" {
		t.Fatalf("lost webhook facts: %+v", event)
	}
}

func TestWebhookIssueCommentAndPullRequestEvents(t *testing.T) {
	for _, test := range []struct{ event, body string }{
		{"issue_comment", `{"action":"created","issue":{"number":42,
			"pull_request":{"url":"https://api.github.com/repos/org/repo/pulls/42"}},"comment":{"id":5,"body":"mention"}}`},
		{"issue_comment", `{"action":"created","issue":{"number":42},"comment":{"id":5}}`},
		{"pull_request", `{"action":"synchronize","before":"old","after":"new","pull_request":{"number":42}}`},
		{"pull_request", `{"action":"opened","pull_request":{"number":42}}`},
		{"installation", `{"action":"deleted","installation":{"id":456}}`},
		{"ping", `{"zen":"Keep it logically awesome."}`},
		{"future_event", `{"action":"future"}`},
	} {
		t.Run(test.event+test.body, func(t *testing.T) {
			body := []byte(test.body)
			header := webhookHeaders(body)
			header.Set(EventHeader, test.event)
			event, err := DecodeWebhook(header, body, "test-secret")
			if err != nil || event.EventType != test.event {
				t.Fatalf("event = %+v, error = %v", event, err)
			}
			if test.event == "issue_comment" && (event.Issue.PullRequest != nil) != strings.Contains(test.body, "pull_request") {
				t.Fatal("lost issue vs PR distinction")
			}
		})
	}
}

func TestWebhookRejectsInvalidSignatureHeadersAndBody(t *testing.T) {
	body := []byte(`{"action":"created"}`)
	for _, change := range []func(http.Header){
		func(h http.Header) { h.Del(SignatureHeader) },
		func(h http.Header) { h.Set(SignatureHeader, "sha1=bad") },
		func(h http.Header) { h.Set(SignatureHeader, "sha256=00") },
		func(h http.Header) { h.Set(SignatureHeader, "sha256="+strings.Repeat("z", 64)) },
		func(h http.Header) { h.Add(SignatureHeader, h.Get(SignatureHeader)) },
		func(h http.Header) { h.Del(EventHeader) },
		func(h http.Header) { h.Del(DeliveryHeader) },
		func(h http.Header) { h.Add(EventHeader, "issue_comment") },
		func(h http.Header) { h.Set(DeliveryHeader, "bad\r\nheader") },
		func(h http.Header) { h.Set("X-Github-Hook-Id", "-1") },
		func(h http.Header) { h.Set("X-Github-Hook-Installation-Target-Id", "not-number") },
	} {
		header := webhookHeaders(body)
		change(header)
		if _, err := DecodeWebhook(header, body, "test-secret"); err == nil {
			t.Fatal("accepted invalid webhook headers")
		}
	}
	for _, body := range []string{`null`, `[]`, `{} {}`, `{"comment":`, strings.Repeat("x", EventBodyMaxBytes+1)} {
		raw := []byte(body)
		if _, err := DecodeWebhook(webhookHeaders(raw), raw, "test-secret"); err == nil {
			t.Fatal("accepted invalid or oversized webhook body")
		}
	}
	if _, err := DecodeWebhook(webhookHeaders(body), body, "wrong-secret"); err == nil {
		t.Fatal("accepted wrong webhook secret")
	}
}
