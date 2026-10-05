package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/ssrf"
)

func TestHTTPClientBlocksInternalAddresses(t *testing.T) {
	for _, target := range []string{
		"https://10.0.0.1",
		"http://169.254.170.2",
		"http://127.0.0.1",
		"http://localhost",
	} {
		t.Run(target, func(t *testing.T) {
			_, err := NewHTTPClient().Get(target)
			if !errors.Is(err, ssrf.ErrBlockedAddress) {
				t.Fatalf("request error = %v, want ErrBlockedAddress", err)
			}
		})
	}
}

func TestDoHTTPResponsePreservesHeaders(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "17")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer server.Close()

	response, err := DoHTTPResponse(
		context.Background(),
		server.Client(),
		"test provider",
		http.MethodGet,
		server.URL,
		nil,
		nil,
	)
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	if response.StatusCode != http.StatusTooManyRequests || response.Header.Get("Retry-After") != "17" {
		t.Fatalf("response = %+v, want status and Retry-After", response)
	}
}

func TestRetryAfterErrorPreservesCauseAndHint(t *testing.T) {
	cause := errors.New("rate limited")
	err := WithRetryAfter(cause, http.Header{"Retry-After": []string{"3"}})
	if !errors.Is(err, cause) {
		t.Fatalf("wrapped error = %v, want original cause", err)
	}
	delay, ok := RetryAfter(err)
	if !ok || delay != 3*time.Second {
		t.Fatalf("retry delay = (%s, %t), want (3s, true)", delay, ok)
	}
}

func TestRetryAfterDelayParsesHTTPDateAndRejectsMalformedValues(t *testing.T) {
	now := time.Date(2026, 8, 8, 12, 0, 0, 0, time.UTC)
	delay, ok := retryAfterDelay(now.Add(45*time.Second).Format(http.TimeFormat), now)
	if !ok || delay != 45*time.Second {
		t.Fatalf("HTTP-date delay = (%s, %t), want (45s, true)", delay, ok)
	}
	if _, ok := retryAfterDelay("eventually", now); ok {
		t.Fatal("malformed Retry-After was accepted")
	}
	for _, value := range []string{"-1", "2.5", "Inf", "1e300", "18446744073709551615"} {
		if _, ok := retryAfterDelay(value, now); ok {
			t.Fatalf("invalid Retry-After %q was accepted", value)
		}
	}
}

func TestHTTPClientBlocksRedirects(t *testing.T) {
	client := NewHTTPClient()
	request, err := http.NewRequest(http.MethodGet, "https://api.example.com/next", nil)
	if err != nil {
		t.Fatalf("build request: %v", err)
	}
	if err := client.CheckRedirect(request, nil); !errors.Is(err, http.ErrUseLastResponse) {
		t.Fatalf("redirect error = %v, want ErrUseLastResponse", err)
	}
}

func TestHTTPClientsShareTransport(t *testing.T) {
	first := NewHTTPClient()
	second := NewHTTPClient()
	if first == second {
		t.Fatal("clients must be isolated")
	}
	if first.Transport != second.Transport {
		t.Fatal("clients must share a transport")
	}
}

func TestDoHTTPRequestRejectsOversizedResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", providerMaxResponseBytes+1)))
	}))
	defer server.Close()

	_, err := DoHTTPResponse(
		context.Background(),
		server.Client(),
		"test provider",
		http.MethodGet,
		server.URL,
		nil,
		nil,
	)
	if !errors.Is(err, ErrResponseTooLarge) {
		t.Fatalf("request error = %v, want response limit error", err)
	}
}
