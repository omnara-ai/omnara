package providers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

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

	_, _, err := DoHTTPRequest(
		context.Background(),
		server.Client(),
		"test provider",
		http.MethodGet,
		server.URL,
		nil,
		nil,
	)
	if err == nil || !strings.Contains(err.Error(), "response exceeds the byte limit") {
		t.Fatalf("request error = %v, want response limit error", err)
	}
}
