package mcpregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const clientMaxBody = 4 * 1024 * 1024

var (
	ErrBadRequest  = errors.New("mcp registry rejected request")
	ErrUnavailable = errors.New("mcp registry unavailable")
)

type Client struct {
	baseURL    *url.URL
	httpClient *http.Client
}

func NewClient(baseURL string, httpClient *http.Client) (*Client, error) {
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return nil, fmt.Errorf("parse mcp registry url: %w", err)
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" || parsed.Host == "" {
		return nil, errors.New("mcp registry url must be an absolute http(s) url")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	return &Client{baseURL: parsed, httpClient: httpClient}, nil
}

func (c *Client) Search(ctx context.Context, params SearchParams) (SearchPage, error) {
	query := url.Values{}
	if params.Query != "" {
		query.Set(QueryParam, params.Query)
	}
	if params.RemoteURL != "" {
		query.Set(RemoteURLParam, params.RemoteURL)
	}
	if params.Limit > 0 {
		query.Set(LimitParam, strconv.Itoa(params.Limit))
	}
	if params.Cursor != "" {
		query.Set(CursorParam, params.Cursor)
	}
	endpoint := c.baseURL.JoinPath(ServersPath)
	endpoint.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint.String(), nil)
	if err != nil {
		return SearchPage{}, fmt.Errorf("build mcp registry request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return SearchPage{}, fmt.Errorf("%w: %w", ErrUnavailable, err)
	}
	defer func() { _ = resp.Body.Close() }()
	body := io.LimitReader(resp.Body, clientMaxBody)
	switch {
	case resp.StatusCode == http.StatusOK:
	case resp.StatusCode == http.StatusBadRequest:
		var failure errorBody
		_ = json.NewDecoder(body).Decode(&failure)
		return SearchPage{}, fmt.Errorf("%w: %s", ErrBadRequest, failure.Error)
	default:
		return SearchPage{}, fmt.Errorf("%w: status %d", ErrUnavailable, resp.StatusCode)
	}
	var page SearchPage
	if err := json.NewDecoder(body).Decode(&page); err != nil {
		return SearchPage{}, fmt.Errorf("%w: decode response: %w", ErrUnavailable, err)
	}
	if page.Servers == nil {
		page.Servers = []Server{}
	}
	return page, nil
}
