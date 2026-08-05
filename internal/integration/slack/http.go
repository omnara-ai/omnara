package slack

import (
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

var defaultHTTPClient = outboundhttp.NewPublicClient(
	outboundhttp.PublicClientOptions{Timeout: 30 * time.Second},
)

func httpClientWithoutRedirects(client *http.Client) *http.Client {
	if client == nil {
		return defaultHTTPClient
	}
	return outboundhttp.CloneWithoutRedirects(client)
}

func readResponseBody(body io.Reader, limit int64) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > limit {
		return nil, errors.New("slack response exceeds the byte limit")
	}
	return data, nil
}
