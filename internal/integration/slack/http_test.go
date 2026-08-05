package slack

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/ssrf"
)

func TestDefaultHTTPClientBlocksPrivateDestinations(t *testing.T) {
	_, err := defaultHTTPClient.Get("http://127.0.0.1:1")
	if !errors.Is(err, ssrf.ErrBlockedAddress) {
		t.Fatalf("request error = %v, want ssrf.ErrBlockedAddress", err)
	}
}

func TestHTTPClientWithoutRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	response, err := httpClientWithoutRedirects(source.Client()).Get(source.URL)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusTemporaryRedirect {
		t.Fatalf("status = %d, want %d", response.StatusCode, http.StatusTemporaryRedirect)
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("redirected requests = %d, want 0", redirectedRequests.Load())
	}
}

func TestReadResponseBodyRejectsOverflow(t *testing.T) {
	if _, err := readResponseBody(strings.NewReader("1234"), 3); err == nil {
		t.Fatal("oversized response accepted")
	}
}
