package model_test

import (
	"errors"
	"io"
	"net/http"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
)

type timeoutRoundTrip func(*http.Request) (*http.Response, error)

func (f timeoutRoundTrip) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

func TestAdaptersApplyConfiguredIdleTimeout(t *testing.T) {
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
	for _, tc := range adapterWireClients(wireClientConfig{httpClient: httpClient, idleTimeout: 3 * time.Second}) {
		t.Run(tc.name, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				start := time.Now()
				response, err := tc.client.Respond(t.Context(), model.Request{ProviderRequest: []byte(`{}`)})
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
