//go:build live

package openaichatcompletions

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelenvelope"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

type liveOpenRouterResponseCapture struct {
	body bytes.Buffer
}

func newLiveOpenRouterResponseCapture() (*http.Client, *liveOpenRouterResponseCapture) {
	client := outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{
		DisableResponseHeaderTimeout: true,
		Timeout:                      2 * time.Minute,
	})
	capture := &liveOpenRouterResponseCapture{}
	captureTransport := client.Transport
	client.Transport = roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		response, err := captureTransport.RoundTrip(request)
		if err != nil {
			return nil, err
		}
		response.Body = struct {
			io.Reader
			io.Closer
		}{
			Reader: io.TeeReader(response.Body, &capture.body),
			Closer: response.Body,
		}
		return response, nil
	})
	return client, capture
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type liveOpenRouterUsage struct {
	Cost        json.RawMessage `json:"cost"`
	CostDetails json.RawMessage `json:"cost_details"`
	IsBYOK      *bool           `json:"is_byok"`
}

func assertLiveOpenRouterReportedCost(
	t *testing.T,
	actual modelenvelope.ProviderReportedCostUSD,
	capture *liveOpenRouterResponseCapture,
) *bool {
	t.Helper()
	usage := capturedLiveOpenRouterUsage(t, capture.body.Bytes())
	baseCost := strings.TrimSpace(string(usage.Cost))
	expected, valid := modelenvelope.ParseProviderReportedCostUSD(baseCost)
	if !valid {
		t.Fatalf("live OpenRouter response reported invalid cost %s", usage.Cost)
	}
	if usage.IsBYOK != nil && *usage.IsBYOK {
		var details struct {
			UpstreamInferenceCost json.RawMessage `json:"upstream_inference_cost"`
		}
		if err := json.Unmarshal(usage.CostDetails, &details); err != nil {
			t.Fatalf("decode live OpenRouter cost details: %v", err)
		}
		expected, valid = modelenvelope.SumProviderReportedCostUSD(
			baseCost,
			strings.TrimSpace(string(details.UpstreamInferenceCost)),
		)
		if !valid {
			t.Fatalf("live OpenRouter BYOK response did not report valid upstream cost: %s", usage.CostDetails)
		}
	}
	if actual != expected {
		t.Fatalf("live OpenRouter reported cost = %q, want %q from provider accounting", actual, expected)
	}
	if usage.IsBYOK == nil {
		t.Log("verified live OpenRouter cost (is_byok omitted)")
		return nil
	}
	t.Logf("verified live OpenRouter cost (is_byok=%t)", *usage.IsBYOK)
	return usage.IsBYOK
}

func capturedLiveOpenRouterUsage(t *testing.T, body []byte) liveOpenRouterUsage {
	t.Helper()
	var (
		usage liveOpenRouterUsage
		found bool
	)
	err := route.ReadSSEEvents(context.Background(), bytes.NewReader(body), func(event route.SSEEvent) error {
		data := strings.TrimSpace(event.Data)
		if data == "" || data == "[DONE]" {
			return nil
		}
		var chunk struct {
			Usage *liveOpenRouterUsage `json:"usage"`
		}
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			return err
		}
		if chunk.Usage != nil {
			usage = *chunk.Usage
			found = true
		}
		return nil
	})
	if err != nil {
		t.Fatalf("decode captured live OpenRouter response: %v", err)
	}
	if !found {
		t.Fatal("captured live OpenRouter response did not include usage")
	}
	return usage
}
