package modelbatch

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
)

func sign(secret string, at time.Time, body []byte) string {
	timestamp := strconv.FormatInt(at.Unix(), 10)
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp + ","))
	mac.Write(body)
	return "t=" + timestamp + ",v1=" + hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyOpenRouterSignature(t *testing.T) {
	now := time.Unix(1_900_000_000, 0)
	body := []byte(`{"id":"evt_1","type":"batch.completed","data":{"id":"batch_123"}}`)
	valid := sign("secret", now, body)
	if err := VerifyOpenRouterSignature("secret", valid, body, now.Add(time.Minute)); err != nil {
		t.Fatalf("valid signature rejected: %v", err)
	}
	for name, test := range map[string]struct {
		header string
		body   []byte
		now    time.Time
	}{
		"missing header":   {header: "", body: body, now: now},
		"wrong secret":     {header: sign("other", now, body), body: body, now: now},
		"tampered body":    {header: valid, body: append([]byte("x"), body...), now: now},
		"stale timestamp":  {header: valid, body: body, now: now.Add(6 * time.Minute)},
		"future timestamp": {header: valid, body: body, now: now.Add(-6 * time.Minute)},
		"not hex":          {header: "t=" + strconv.FormatInt(now.Unix(), 10) + ",v1=zz", body: body, now: now},
	} {
		err := VerifyOpenRouterSignature("secret", test.header, test.body, test.now)
		if !errors.Is(err, ErrInvalidSignature) {
			t.Errorf("%s: err = %v, want invalid signature", name, err)
		}
	}
	if err := VerifyOpenRouterSignature("", valid, body, now); err == nil {
		t.Error("empty secret accepted a signature")
	}
}

func TestCallbackTokenAndURL(t *testing.T) {
	token, hash, err := NewCallbackToken()
	if err != nil {
		t.Fatalf("new token: %v", err)
	}
	other, _, _ := NewCallbackToken()
	if len(token) != 64 || token == other || !bytes.Equal(hash, HashCallbackToken(token)) ||
		bytes.Equal(hash, HashCallbackToken(other)) {
		t.Fatalf("callback tokens are not unique capability tokens: %q %q", token, other)
	}
	id := uuid.New()
	got := OpenRouterCallbackURL("https://omnara.example/", id, token)
	want := "https://omnara.example/api/model-batches/openrouter/" + id.String() + "/" + token
	if got != want {
		t.Fatalf("callback url = %q, want %q", got, want)
	}
	pattern := strings.NewReplacer("{batch_request_id}", id.String(), "{token}", token).
		Replace(OpenRouterCallbackPathPattern)
	if !strings.HasSuffix(got, pattern) {
		t.Fatalf("callback url %q does not match the served route %q", got, pattern)
	}
	// OpenRouter rejects callback URLs that are not public HTTPS; fall back to polling.
	for _, publicURL := range []string{"", "http://localhost:8080"} {
		if got := OpenRouterCallbackURL(publicURL, id, token); got != "" {
			t.Errorf("OpenRouterCallbackURL(%q) = %q, want none", publicURL, got)
		}
	}
}
