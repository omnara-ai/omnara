package discord

import (
	"bytes"
	"context"
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
	APIURL           = "https://discord.com/api/v10"
	OperationTimeout = 15 * time.Second
	ResponseMaxBytes = 2 * 1024 * 1024
	ErrorMaxBytes    = 8 * 1024
)

type Credentials struct {
	ApplicationID string
	BotUserID     string
	BotToken      string
}

type Config struct {
	Credentials   Credentials
	HTTPClient    *http.Client
	APIURL        string
	BeforeRequest func(context.Context) error
}

type Client struct {
	http          *http.Client
	base          string
	credentials   Credentials
	beforeRequest func(context.Context) error
}

func NewClient(config Config) (*Client, error) {
	if !validID(config.Credentials.ApplicationID) || !validID(config.Credentials.BotUserID) {
		return nil, errors.New("discord requires application ID, bot user ID and bot token")
	}
	return newClient(config)
}

func newClient(config Config) (*Client, error) {
	if config.Credentials.BotToken == "" || strings.ContainsAny(config.Credentials.BotToken, " \r\n\t") {
		return nil, errors.New("discord requires bot token")
	}
	raw := config.APIURL
	if raw == "" {
		raw = APIURL
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.ForceQuery ||
		u.Fragment != "" || u.RawPath != "" || u.Path != "/api/v10" {
		return nil, errors.New("invalid discord API URL")
	}
	loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
	if u.Scheme != "https" && !(u.Scheme == "http" && loopback && config.HTTPClient != nil) {
		return nil, errors.New("discord API requires HTTPS")
	}
	client := config.HTTPClient
	if client == nil {
		client = outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{Timeout: OperationTimeout})
	} else {
		client = outboundhttp.CloneWithoutRedirects(client)
		if client.Timeout <= 0 || client.Timeout > OperationTimeout {
			client.Timeout = OperationTimeout
		}
	}
	return &Client{http: client, base: u.String(), credentials: config.Credentials,
		beforeRequest: config.BeforeRequest}, nil
}

func (c *Client) json(ctx context.Context, method, path string, input, output any, retrySend bool) error {
	var body []byte
	if input != nil {
		var err error
		body, err = json.Marshal(input)
		if err != nil {
			return errors.New("invalid discord request")
		}
	}
	data, err := c.do(ctx, method, c.base+path, "application/json", body, true, retrySend, ResponseMaxBytes)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, output); err != nil {
		return invalidResponse(method != http.MethodGet)
	}
	return nil
}

func (c *Client) do(ctx context.Context, method, target, contentType string, body []byte,
	auth, retrySend bool, limit int64,
) ([]byte, error) {
	mutation := method != http.MethodGet
	var uncertain bool
	for attempt := range 3 {
		if err := ctx.Err(); err != nil {
			if uncertain {
				return nil, &APIError{Code: DeliveryUnknown, cause: err}
			}
			return nil, err
		}
		req, err := http.NewRequestWithContext(ctx, method, target, bytes.NewReader(body))
		if err != nil {
			return nil, errors.New("invalid discord request")
		}
		if auth {
			req.Header.Set("Authorization", "Bot "+c.credentials.BotToken)
		}
		req.Header.Set("User-Agent", "DiscordBot (https://omnara.com, 1)")
		if body != nil {
			req.Header.Set("Content-Type", contentType)
		}
		if c.beforeRequest != nil {
			if err := c.beforeRequest(ctx); err != nil {
				if uncertain {
					return nil, &APIError{Code: DeliveryUnknown}
				}
				return nil, err
			}
		}
		data, apiErr := c.attempt(req, mutation, limit)
		if apiErr == nil {
			return data, nil
		}
		uncertain = uncertain || apiErr.Code == DeliveryUnknown
		// A 429 is definitely unsent. Keep retries short; longer waits belong to
		// the durable caller, which receives the provider's RetryAfter hint.
		canRetry := apiErr.Code == RateLimited || apiErr.Code == TransientFailure ||
			(retrySend && apiErr.Code == DeliveryUnknown)
		if (!mutation || retrySend) && canRetry && attempt < 2 && ctx.Err() == nil && apiErr.RetryAfter <= time.Second {
			delay := max(time.Duration(attempt+1)*100*time.Millisecond, apiErr.RetryAfter)
			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
			case <-timer.C:
			}
			continue
		}
		if uncertain && apiErr.Code != DeliveryUnknown {
			return nil, &APIError{Code: DeliveryUnknown, StatusCode: apiErr.StatusCode,
				RetryAfter: apiErr.RetryAfter, Global: apiErr.Global, cause: apiErr.cause}
		}
		return nil, apiErr
	}
	return nil, &APIError{Code: TransientFailure}
}

func (c *Client) attempt(req *http.Request, mutation bool, limit int64) ([]byte, *APIError) {
	resp, err := c.http.Do(req)
	if err != nil {
		code := TransientFailure
		if mutation {
			code = DeliveryUnknown
		}
		return nil, &APIError{Code: code, cause: contextCause(err)}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, ErrorMaxBytes))
		return nil, responseError(resp.StatusCode, resp.Header, body, mutation)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		result := invalidResponse(mutation)
		result.cause = contextCause(err)
		return nil, result
	}
	return data, nil
}
