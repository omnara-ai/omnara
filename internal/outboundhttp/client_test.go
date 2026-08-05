package outboundhttp

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/ssrf"
)

func TestPublicClientBlocksPrivateDestinations(t *testing.T) {
	client := NewPublicClient(PublicClientOptions{Timeout: time.Second})
	_, err := client.Get("http://127.0.0.1:1")
	if !errors.Is(err, ssrf.ErrBlockedAddress) {
		t.Fatalf("request error = %v, want ErrBlockedAddress", err)
	}
}

func TestPublicClientDoesNotUseAmbientProxy(t *testing.T) {
	client := NewPublicClient(PublicClientOptions{})
	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport type = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("public client uses an ambient proxy")
	}
}

func TestPublicClientDoesNotFollowRedirects(t *testing.T) {
	var redirectedRequests atomic.Int32
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	defer target.Close()
	source := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer source.Close()

	client := NewPublicClient(PublicClientOptions{AllowLoopback: true, Timeout: time.Second})
	response, err := client.Get(source.URL)
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

func TestRejectedRedirectsReturnTypedError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/target", http.StatusTemporaryRedirect)
	}))
	defer server.Close()

	client := CloneRejectingRedirects(server.Client())
	response, err := client.Get(server.URL)
	if response != nil {
		_ = response.Body.Close()
	}
	if !errors.Is(err, ErrRedirect) {
		t.Fatalf("request error = %v, want ErrRedirect", err)
	}
}

func TestCloneWithoutRedirectsDoesNotMutateInput(t *testing.T) {
	client := &http.Client{Timeout: time.Second}
	secured := CloneWithoutRedirects(client)
	if client.CheckRedirect != nil {
		t.Fatal("input client was mutated")
	}
	if secured == client || secured.Timeout != client.Timeout {
		t.Fatal("secured client must be a policy-preserving clone")
	}
}
