package mcpregistry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	DefaultUpstreamURL = "https://registry.modelcontextprotocol.io"
	upstreamPageSize   = 100
	upstreamMaxBody    = 32 * 1024 * 1024
	officialMetaKey    = "io.modelcontextprotocol.registry/official"
	activeStatus       = "active"
	streamableHTTPType = "streamable-http"

	DefaultBatchPages  = 10
	DefaultBatchDelay  = time.Second
	DefaultMaxAttempts = 6
	DefaultBaseBackoff = time.Second
	DefaultMaxBackoff  = 30 * time.Second
	maxRetryAfter      = 2 * time.Minute
)

var ErrUpstreamStatus = errors.New("unexpected upstream status")

type retryableError struct {
	err        error
	retryAfter time.Duration
}

func (e retryableError) Error() string { return e.err.Error() }
func (e retryableError) Unwrap() error { return e.err }

type upstreamPage struct {
	Servers  []upstreamEntry `json:"servers"`
	Metadata struct {
		NextCursor string `json:"nextCursor"`
	} `json:"metadata"`
}

type upstreamEntry struct {
	Server struct {
		Name        string           `json:"name"`
		Title       string           `json:"title"`
		Description string           `json:"description"`
		Version     string           `json:"version"`
		WebsiteURL  string           `json:"websiteUrl"`
		Remotes     []upstreamRemote `json:"remotes"`
		Icons       []upstreamIcon   `json:"icons"`
	} `json:"server"`
	Meta map[string]struct {
		Status    string    `json:"status"`
		UpdatedAt time.Time `json:"updatedAt"`
		IsLatest  bool      `json:"isLatest"`
	} `json:"_meta"`
}

type upstreamIcon struct {
	Src      string   `json:"src"`
	MimeType string   `json:"mimeType"`
	Sizes    []string `json:"sizes"`
	Theme    string   `json:"theme"`
}

type upstreamRemote struct {
	Type    string `json:"type"`
	URL     string `json:"url"`
	Headers []struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		IsRequired  bool   `json:"isRequired"`
		IsSecret    bool   `json:"isSecret"`
	} `json:"headers"`
}

type Syncer struct {
	UpstreamURL string
	HTTPClient  *http.Client
	BatchPages  int
	BatchDelay  time.Duration
	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	Sleep       func(context.Context, time.Duration) error
}

func (s Syncer) withDefaults() Syncer {
	if s.UpstreamURL == "" {
		s.UpstreamURL = DefaultUpstreamURL
	}
	if s.HTTPClient == nil {
		s.HTTPClient = &http.Client{Timeout: 60 * time.Second}
	}
	if s.BatchPages <= 0 {
		s.BatchPages = DefaultBatchPages
	}
	if s.BatchDelay <= 0 {
		s.BatchDelay = DefaultBatchDelay
	}
	if s.MaxAttempts <= 0 {
		s.MaxAttempts = DefaultMaxAttempts
	}
	if s.BaseBackoff <= 0 {
		s.BaseBackoff = DefaultBaseBackoff
	}
	if s.MaxBackoff <= 0 {
		s.MaxBackoff = DefaultMaxBackoff
	}
	if s.Sleep == nil {
		s.Sleep = sleepContext
	}
	return s
}

func (s Syncer) Fetch(ctx context.Context) ([]Server, error) {
	s = s.withDefaults()
	endpoint, err := url.Parse(s.UpstreamURL)
	if err != nil {
		return nil, fmt.Errorf("parse upstream url: %w", err)
	}
	endpoint = endpoint.JoinPath("v0.1", "servers")
	var servers []Server
	cursor := ""
	for pageIndex := 0; ; pageIndex++ {
		if pageIndex > 0 && pageIndex%s.BatchPages == 0 {
			if err := s.Sleep(ctx, s.BatchDelay); err != nil {
				return nil, err
			}
		}
		page, err := s.fetchPageWithRetry(ctx, endpoint, cursor)
		if err != nil {
			return nil, err
		}
		for _, entry := range page.Servers {
			server, ok := serverFromUpstream(entry)
			if ok {
				servers = append(servers, server)
			}
		}
		if page.Metadata.NextCursor == "" {
			break
		}
		cursor = page.Metadata.NextCursor
	}
	if len(servers) == 0 {
		return nil, errors.New("upstream registry returned no servers")
	}
	return servers, nil
}

func (s Syncer) fetchPageWithRetry(ctx context.Context, endpoint *url.URL, cursor string) (upstreamPage, error) {
	for attempt := 1; ; attempt++ {
		page, err := fetchPage(ctx, s.HTTPClient, endpoint, cursor)
		if err == nil {
			return page, nil
		}
		var retryable retryableError
		if !errors.As(err, &retryable) || attempt >= s.MaxAttempts {
			return upstreamPage{}, fmt.Errorf("after %d attempt(s): %w", attempt, err)
		}
		if err := s.Sleep(ctx, s.backoff(attempt, retryable.retryAfter)); err != nil {
			return upstreamPage{}, err
		}
	}
}

func (s Syncer) backoff(attempt int, retryAfter time.Duration) time.Duration {
	if retryAfter > 0 {
		return min(retryAfter, maxRetryAfter)
	}
	delay := s.BaseBackoff
	for i := 1; i < attempt && delay < s.MaxBackoff; i++ {
		delay *= 2
	}
	delay = min(delay, s.MaxBackoff)
	jitter := time.Duration(rand.Int64N(int64(delay)/2 + 1))
	return delay/2 + jitter
}

func sleepContext(ctx context.Context, delay time.Duration) error {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func parseRetryAfter(header string, now time.Time) time.Duration {
	if header == "" {
		return 0
	}
	if seconds, err := strconv.Atoi(header); err == nil {
		if seconds <= 0 {
			return 0
		}
		return time.Duration(seconds) * time.Second
	}
	if at, err := http.ParseTime(header); err == nil {
		return max(at.Sub(now), 0)
	}
	return 0
}

func fetchPage(ctx context.Context, client *http.Client, endpoint *url.URL, cursor string) (upstreamPage, error) {
	query := url.Values{}
	query.Set("version", "latest")
	query.Set("limit", fmt.Sprint(upstreamPageSize))
	if cursor != "" {
		query.Set("cursor", cursor)
	}
	pageURL := *endpoint
	pageURL.RawQuery = query.Encode()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, pageURL.String(), nil)
	if err != nil {
		return upstreamPage{}, fmt.Errorf("build upstream request: %w", err)
	}
	req.Header.Set("Accept", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return upstreamPage{}, ctx.Err()
		}
		return upstreamPage{}, retryableError{err: fmt.Errorf("fetch upstream page: %w", err)}
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		statusErr := fmt.Errorf("fetch upstream page: %w %d", ErrUpstreamStatus, resp.StatusCode)
		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= http.StatusInternalServerError {
			return upstreamPage{}, retryableError{
				err:        statusErr,
				retryAfter: parseRetryAfter(resp.Header.Get("Retry-After"), time.Now()),
			}
		}
		return upstreamPage{}, statusErr
	}
	var page upstreamPage
	if err := json.NewDecoder(io.LimitReader(resp.Body, upstreamMaxBody)).Decode(&page); err != nil {
		return upstreamPage{}, fmt.Errorf("decode upstream page: %w", err)
	}
	return page, nil
}

func serverFromUpstream(entry upstreamEntry) (Server, bool) {
	official, ok := entry.Meta[officialMetaKey]
	if !ok || !official.IsLatest || official.Status != activeStatus || entry.Server.Name == "" {
		return Server{}, false
	}
	remotes := make([]Remote, 0, len(entry.Server.Remotes))
	for _, remote := range entry.Server.Remotes {
		if remote.Type != streamableHTTPType || remote.URL == "" {
			continue
		}
		headers := make([]Header, 0, len(remote.Headers))
		for _, header := range remote.Headers {
			headers = append(headers, Header{
				Name:        header.Name,
				Description: header.Description,
				IsRequired:  header.IsRequired,
				IsSecret:    header.IsSecret,
			})
		}
		remotes = append(remotes, Remote{Type: remote.Type, URL: remote.URL, Headers: headers})
	}
	if len(remotes) == 0 {
		return Server{}, false
	}
	icons := make([]Icon, 0, len(entry.Server.Icons))
	for _, icon := range entry.Server.Icons {
		if icon.Src == "" {
			continue
		}
		icons = append(icons, Icon(icon))
	}
	return Server{
		Name:        entry.Server.Name,
		Title:       entry.Server.Title,
		Description: entry.Server.Description,
		Version:     entry.Server.Version,
		WebsiteURL:  entry.Server.WebsiteURL,
		Status:      official.Status,
		UpdatedAt:   official.UpdatedAt,
		Remotes:     remotes,
		Icons:       icons,
	}, true
}
