package model_test

import (
	"errors"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/openaichatcompletions"
	"github.com/omnara-ai/omnara/internal/model/openairesponses"
)

type timeoutRoundTrip func(*http.Request) (*http.Response, error)

func (f timeoutRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAdaptersApplyConfiguredIdleTimeout(t *testing.T) {
	for _, format := range []string{"responses", "chat", "anthropic"} {
		t.Run(format, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				httpClient := &http.Client{
					Timeout: time.Minute,
					Transport: timeoutRoundTrip(func(req *http.Request) (*http.Response, error) {
						reader, writer := io.Pipe()
						go func() { <-req.Context().Done(); _ = writer.CloseWithError(req.Context().Err()) }()
						return &http.Response{
							StatusCode: http.StatusOK,
							Header:     http.Header{"Content-Type": {"text/event-stream"}},
							Body:       reader,
						}, nil
					}),
				}
				var client model.Client
				switch format {
				case "responses":
					client = openairesponses.Client{
						BaseURL:      "https://example.test",
						EndpointPath: "/responses",
						HTTPClient:   httpClient,
						IdleTimeout:  3 * time.Second,
					}
				case "chat":
					client = openaichatcompletions.Client{
						BaseURL:      "https://example.test",
						EndpointPath: "/chat/completions",
						HTTPClient:   httpClient,
						IdleTimeout:  3 * time.Second,
					}
				case "anthropic":
					client = anthropicmessages.Client{
						BaseURL:      "https://example.test",
						EndpointPath: "/messages",
						HTTPClient:   httpClient,
						IdleTimeout:  3 * time.Second,
					}
				}
				start := time.Now()
				response, err := client.Respond(t.Context(), model.Request{ProviderRequest: []byte(`{}`)})
				classified, ok := model.ClassifyError(err)
				if !ok || classified.Kind != model.ErrorKindTransient || classified.Code != "provider_idle_timeout" ||
					errors.Is(err, io.EOF) {
					t.Fatalf("classification=%+v error=%v", classified, err)
				}
				if time.Since(start) != 3*time.Second || response.StopReason == model.StopReasonMaxTokens ||
					len(response.Content) != 0 {
					t.Fatalf("elapsed=%s response=%+v", time.Since(start), response)
				}
			})
		})
	}
}
