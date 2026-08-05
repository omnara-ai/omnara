package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const (
	HTTPClientTimeout        = 10 * time.Second
	providerMaxResponseBytes = 1024 * 1024
)

var providerHTTPClient = outboundhttp.NewPublicClient(
	outboundhttp.PublicClientOptions{Timeout: HTTPClientTimeout},
)

func NewHTTPClient() *http.Client {
	client := *providerHTTPClient
	return &client
}

func IsHTTPS(parsed *url.URL) bool {
	return parsed.Scheme == "https"
}

func DoHTTPRequest(
	ctx context.Context,
	httpClient *http.Client,
	provider, method, requestURL string,
	headers map[string]string,
	body any,
) (int, []byte, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return 0, nil, fmt.Errorf("marshal %s request: %w", provider, err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return 0, nil, fmt.Errorf("build %s request: %w", provider, err)
	}
	request.Header.Set("Accept", "application/json")
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := httpClient.Do(request)
	if err != nil {
		return 0, nil, fmt.Errorf("%s request failed: %w", provider, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, providerMaxResponseBytes+1))
	if err != nil {
		return 0, nil, fmt.Errorf("read %s response: %w", provider, err)
	}
	if len(raw) > providerMaxResponseBytes {
		return 0, nil, fmt.Errorf("%s response exceeds the byte limit", provider)
	}
	return response.StatusCode, raw, nil
}
