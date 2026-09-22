package github

import (
	"bytes"
	"context"
	"crypto/rsa"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

const (
	APIURL           = "https://api.github.com"
	APIVersion       = "2026-03-10"
	OperationTimeout = 15 * time.Second
	ResponseMaxBytes = 2 * 1024 * 1024
	ErrorMaxBytes    = 8 * 1024
	jsonMediaType    = "application/vnd.github+json"
)

type Config struct {
	Credentials    Credentials
	InstallationID int64
	HTTPClient     *http.Client
	// BeforeRequest optionally revalidates caller authority immediately before
	// every HTTP attempt, including token requests and retries. Its trusted error
	// is returned unchanged without sending the request. It shares the operation
	// context and must be safe for concurrent use.
	BeforeRequest func(context.Context) error
	// APIURL is a trusted deployment/test setting, never a tool argument. Empty
	// uses GitHub.com. Custom origins require HTTPS, except loopback test servers.
	APIURL string
}

// Client is safe for concurrent use and belongs to one credential revision and
// installation. All pagination and redirects remain confined to the chosen API.
type Client struct {
	*appClient
	installationID int64
	tokenGate      chan struct{}
	tokens         [2]cachedToken
}

// appClient shares bounded transport and App JWT signing with setup. It has no
// installation authority or installation token cache.
type appClient struct {
	http          *http.Client
	base          *url.URL
	appID         int64
	privateKey    *rsa.PrivateKey
	beforeRequest func(context.Context) error
	now           func() time.Time
}

func NewClient(config Config) (*Client, error) {
	if config.Credentials.AppID <= 0 || config.InstallationID <= 0 || config.Credentials.WebhookSecret == "" {
		return nil, errors.New("github requires app ID, installation ID and webhook secret")
	}
	app, err := newAppClient(SetupConfig{
		Credentials: config.Credentials, HTTPClient: config.HTTPClient,
		APIURL: config.APIURL, BeforeRequest: config.BeforeRequest,
	})
	if err != nil {
		return nil, err
	}
	return &Client{
		appClient: app, installationID: config.InstallationID, tokenGate: make(chan struct{}, 1),
	}, nil
}

func newAppClient(config SetupConfig) (*appClient, error) {
	var key *rsa.PrivateKey
	if config.Credentials != (Credentials{}) {
		if config.Credentials.AppID <= 0 || config.Credentials.WebhookSecret == "" {
			return nil, errors.New("github requires app ID, private key and webhook secret")
		}
		var err error
		key, err = parsePrivateKey(config.Credentials.PrivateKeyPEM)
		if err != nil {
			return nil, err
		}
	}
	raw := config.APIURL
	if raw == "" {
		raw = APIURL
	}
	base, err := url.Parse(raw)
	if err != nil || base.Host == "" || base.User != nil || base.RawQuery != "" || base.ForceQuery ||
		base.Fragment != "" || base.RawPath != "" || (base.Path != "" && base.Path != "/") {
		return nil, errors.New("invalid github API origin")
	}
	loopback := base.Hostname() == "127.0.0.1" || base.Hostname() == "localhost" || base.Hostname() == "::1"
	if base.Scheme != "https" && !(base.Scheme == "http" && loopback && config.HTTPClient != nil) {
		return nil, errors.New("github API origin requires HTTPS")
	}
	base.Path = ""
	client := config.HTTPClient
	if client == nil {
		client = outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{Timeout: OperationTimeout})
	} else {
		client = outboundhttp.CloneWithoutRedirects(client)
		if client.Timeout == 0 || client.Timeout > OperationTimeout {
			client.Timeout = OperationTimeout
		}
	}
	return &appClient{
		http: client, base: base, appID: config.Credentials.AppID,
		privateKey: key, beforeRequest: config.BeforeRequest, now: time.Now,
	}, nil
}

func (c *Client) request(
	ctx context.Context, token, method, path string, input, output any,
) (http.Header, error) {
	mutation := method == http.MethodPost
	header, err := c.doJSON(ctx, method, path, token, input, output, mutation)
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnauthorized {
		c.invalidateToken(ctx, token)
	}
	return header, err
}

func (c *appClient) doJSON(
	ctx context.Context, method, path, token string, input, output any, mutation bool,
) (http.Header, error) {
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return nil, errors.New("invalid github request body")
		}
	}
	data, header, err := c.do(ctx, method, path, token, jsonMediaType, body, mutation)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, output); err != nil {
		code := InvalidResponse
		if mutation {
			code = DeliveryUnknown
		}
		return nil, &APIError{Code: code}
	}
	return header, nil
}

func (c *appClient) do(
	ctx context.Context, method, path, token, accept string, body []byte, mutation bool,
) ([]byte, http.Header, error) {
	// Paths originate only from the typed methods below, not provider URLs.
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") {
		return nil, nil, errors.New("invalid github API path")
	}
	for attempt := range 3 {
		if err := ctx.Err(); err != nil {
			return nil, nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, c.base.String()+path, bytes.NewReader(body))
		if err != nil {
			return nil, nil, errors.New("invalid github request")
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		req.Header.Set("Accept", accept)
		req.Header.Set("X-Github-Api-Version", APIVersion)
		req.Header.Set("User-Agent", "Omnara-GitHub-App")
		if body != nil {
			req.Header.Set("Content-Type", "application/json")
		}
		if c.beforeRequest != nil {
			if err := c.beforeRequest(ctx); err != nil {
				return nil, nil, err
			}
		}
		data, header, apiErr := c.attempt(req, mutation)
		if apiErr == nil {
			return data, header, nil
		}
		if method != http.MethodGet || apiErr.Code != TransientFailure || attempt == 2 || ctx.Err() != nil {
			return nil, nil, apiErr
		}
		delay := max(time.Duration(attempt+1)*100*time.Millisecond, apiErr.RetryAfter)
		// Long provider delays belong to the caller's scheduler, not this tool.
		if delay > time.Second {
			return nil, nil, apiErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, nil, ctx.Err()
		case <-timer.C:
		}
	}
	return nil, nil, &APIError{Code: TransientFailure}
}

func (c *appClient) attempt(req *http.Request, mutation bool) ([]byte, http.Header, *APIError) {
	resp, err := c.http.Do(req)
	if err != nil {
		code := TransientFailure
		if mutation {
			code = DeliveryUnknown
		}
		return nil, nil, &APIError{Code: code, cause: contextCause(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, ErrorMaxBytes))
		return nil, nil, responseError(resp.StatusCode, resp.Header, body, mutation, c.now())
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, ResponseMaxBytes+1))
	if err != nil || len(body) > ResponseMaxBytes {
		code := InvalidResponse
		if mutation {
			code = DeliveryUnknown
		}
		return nil, nil, &APIError{Code: code, StatusCode: resp.StatusCode, cause: contextCause(err)}
	}
	return body, resp.Header, nil
}
