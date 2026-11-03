package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const (
	HTTPClientTimeout = 10 * time.Second
	// One managed sandbox may contain a 1 MiB environment. Eight MiB keeps the
	// response bounded while allowing for JSON escaping and provider metadata.
	providerMaxResponseBytes = 8 * 1024 * 1024
)

var ErrResponseTooLarge = errors.New("provider response exceeds the byte limit")

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

type HTTPResponse struct {
	StatusCode int
	Header     http.Header
	Body       []byte
}

func DoHTTPResponse(
	ctx context.Context,
	httpClient *http.Client,
	provider, method, requestURL string,
	headers map[string]string,
	body any,
) (HTTPResponse, error) {
	var reader io.Reader
	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			return HTTPResponse{}, fmt.Errorf("marshal %s request: %w", provider, err)
		}
		reader = bytes.NewReader(raw)
	}
	request, err := http.NewRequestWithContext(ctx, method, requestURL, reader)
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("build %s request: %w", provider, err)
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
		return HTTPResponse{}, fmt.Errorf("%s request failed: %w", provider, err)
	}
	defer func() { _ = response.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(response.Body, providerMaxResponseBytes+1))
	if err != nil {
		return HTTPResponse{}, fmt.Errorf("read %s response: %w", provider, err)
	}
	if len(raw) > providerMaxResponseBytes {
		return HTTPResponse{}, fmt.Errorf("%s: %w", provider, ErrResponseTooLarge)
	}
	return HTTPResponse{
		StatusCode: response.StatusCode,
		Header:     response.Header.Clone(),
		Body:       raw,
	}, nil
}

type responseError struct {
	err           error
	statusCode    int
	retryAfter    time.Duration
	hasRetryAfter bool
}

func (e responseError) Error() string {
	return e.err.Error()
}

func (e responseError) Unwrap() error {
	return e.err
}

func WithResponse(err error, response HTTPResponse) error {
	if err == nil {
		return nil
	}
	retryAfter, hasRetryAfter := retryAfterDelay(response.Header.Get("Retry-After"), time.Now())
	return responseError{
		err:           err,
		statusCode:    response.StatusCode,
		retryAfter:    retryAfter,
		hasRetryAfter: hasRetryAfter,
	}
}

func HTTPStatus(err error) (int, bool) {
	var responseErr responseError
	if !errors.As(err, &responseErr) {
		return 0, false
	}
	return responseErr.statusCode, true
}

// RetryAfter returns a delay carried by any wrapped provider error.
func RetryAfter(err error) (time.Duration, bool) {
	var responseErr responseError
	if !errors.As(err, &responseErr) || !responseErr.hasRetryAfter {
		return 0, false
	}
	return max(responseErr.retryAfter, 0), true
}

func retryAfterDelay(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		const maxDuration = time.Duration(math.MaxInt64)
		if seconds > uint64(maxDuration/time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	when, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	return max(when.Sub(now), 0), true
}
