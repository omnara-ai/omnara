package arker

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
)

func TestArkerRESTClientErrorsCarryRetryAfterWithoutTheResponseBody(t *testing.T) {
	authorizations := make(chan string, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		authorizations <- r.Header.Get("Authorization")
		w.Header().Set("Retry-After", "7")
		w.WriteHeader(http.StatusServiceUnavailable)
		fmt.Fprint(w, `{"error":{"code":"unavailable","message":"provider secret detail"}}`)
	}))
	t.Cleanup(server.Close)
	client := &restClient{baseURL: server.URL, apiToken: "ark_test", httpClient: server.Client()}

	_, err := client.Fork(context.Background(), forkRequest{}, "key")
	var apiErr apiError
	if !errors.As(err, &apiErr) || apiErr.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("fork error = %v", err)
	}
	if strings.Contains(err.Error(), "secret detail") {
		t.Fatalf("error exposes the response body: %v", err)
	}
	if delay, ok := providers.RetryAfter(err); !ok || delay != 7*time.Second {
		t.Fatalf("retry after = %v, %v", delay, ok)
	}
	if authorization := <-authorizations; authorization != "Bearer ark_test" {
		t.Fatalf("authorization = %q", authorization)
	}
}
