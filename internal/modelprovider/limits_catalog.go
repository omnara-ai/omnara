package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

const (
	openRouterCatalogURL       = "https://openrouter.ai/api/v1/models"
	catalogRequestTimeout      = 8 * time.Second
	catalogMaxResponseSize     = 16 * 1024 * 1024
	catalogRefreshInterval     = time.Hour
	catalogFailureRetryBackoff = time.Minute
)

var catalogDateSuffixPattern = regexp.MustCompile(`-\d{4}-\d{2}-\d{2}$`)

type catalogLimits struct {
	contextWindowTokens *int
	maxOutputTokens     *int
}

type LimitsCatalog struct {
	url    string
	client *http.Client

	mu           sync.Mutex
	entries      map[string]catalogLimits
	refreshedAt  time.Time
	lastAttempt  time.Time
	attemptError error
}

func NewLimitsCatalog() *LimitsCatalog {
	client := newSSRFHTTPClient(false)
	client.Timeout = catalogRequestTimeout
	return &LimitsCatalog{
		url:    openRouterCatalogURL,
		client: outboundhttp.CloneWithoutRedirects(client),
	}
}

func (c *LimitsCatalog) FillMissingLimits(ctx context.Context, models []DiscoveredModel) []DiscoveredModel {
	if !anyModelMissingContextWindow(models) {
		return models
	}
	entries := c.snapshot(ctx)
	if len(entries) == 0 {
		return models
	}
	for i := range models {
		if models[i].ContextWindowTokens != nil {
			continue
		}
		limits, found := lookupCatalogLimits(entries, models[i].Slug)
		if !found || limits.contextWindowTokens == nil {
			continue
		}
		models[i].ContextWindowTokens = limits.contextWindowTokens
		if models[i].MaxOutputTokens == nil {
			maxOutput := limits.maxOutputTokens
			if maxOutput != nil && *maxOutput >= *limits.contextWindowTokens {
				maxOutput = nil
			}
			models[i].MaxOutputTokens = maxOutput
		}
	}
	return models
}

func anyModelMissingContextWindow(models []DiscoveredModel) bool {
	for _, model := range models {
		if model.ContextWindowTokens == nil {
			return true
		}
	}
	return false
}

func lookupCatalogLimits(entries map[string]catalogLimits, slug string) (catalogLimits, bool) {
	if limits, found := entries[slug]; found {
		return limits, true
	}
	trimmed := catalogDateSuffixPattern.ReplaceAllString(slug, "")
	if trimmed != slug {
		if limits, found := entries[trimmed]; found {
			return limits, true
		}
	}
	return catalogLimits{}, false
}

func (c *LimitsCatalog) snapshot(ctx context.Context) map[string]catalogLimits {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	fresh := c.entries != nil && now.Sub(c.refreshedAt) < catalogRefreshInterval
	recentlyFailed := c.attemptError != nil && now.Sub(c.lastAttempt) < catalogFailureRetryBackoff
	if fresh || recentlyFailed {
		return c.entries
	}
	c.lastAttempt = now
	entries, err := c.fetch(ctx)
	c.attemptError = err
	if err != nil {
		return c.entries
	}
	c.entries = entries
	c.refreshedAt = now
	return c.entries
}

func (c *LimitsCatalog) fetch(ctx context.Context) (map[string]catalogLimits, error) {
	// Detach from the caller's cancelation so an abandoned or short-deadline
	// request cannot poison the shared cache's failure backoff.
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), catalogRequestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("model limits catalog request failed: %w", err)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, catalogMaxResponseSize+1))
	closeErr := resp.Body.Close()
	if err != nil {
		return nil, errors.Join(fmt.Errorf("read model limits catalog response: %w", err), closeErr)
	}
	if len(body) > catalogMaxResponseSize {
		return nil, errors.Join(errors.New("model limits catalog response exceeds the byte limit"), closeErr)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("model limits catalog returned status %d", resp.StatusCode)
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close model limits catalog response: %w", closeErr)
	}
	return parseCatalogEntries(body)
}

func parseCatalogEntries(body []byte) (map[string]catalogLimits, error) {
	var decoded struct {
		Data []discoveredModelEntry `json:"data"`
	}
	if err := json.Unmarshal(body, &decoded); err != nil || decoded.Data == nil {
		return nil, errors.New("model limits catalog returned an unrecognized response")
	}
	entries := make(map[string]catalogLimits, len(decoded.Data)*2)
	ambiguous := make(map[string]struct{})
	for _, entry := range decoded.Data {
		slug := strings.TrimSpace(entry.ID)
		contextWindowTokens := entry.contextWindowTokens()
		if slug == "" || contextWindowTokens == nil {
			continue
		}
		limits := catalogLimits{
			contextWindowTokens: contextWindowTokens,
			maxOutputTokens:     entry.maxOutputTokens(),
		}
		entries[slug] = limits
		_, bare, hasPrefix := strings.Cut(slug, "/")
		if !hasPrefix || bare == "" {
			continue
		}
		if _, dropped := ambiguous[bare]; dropped {
			continue
		}
		if existing, found := entries[bare]; found && !sameCatalogLimits(existing, limits) {
			delete(entries, bare)
			ambiguous[bare] = struct{}{}
			continue
		}
		entries[bare] = limits
	}
	if len(entries) == 0 {
		// Treat a degenerate success as a failure so it cannot replace a warm
		// cache and gets the failure retry backoff instead of the refresh TTL.
		return nil, errors.New("model limits catalog returned no usable entries")
	}
	return entries, nil
}

func sameCatalogLimits(a, b catalogLimits) bool {
	return equalIntPtr(a.contextWindowTokens, b.contextWindowTokens) &&
		equalIntPtr(a.maxOutputTokens, b.maxOutputTokens)
}

func equalIntPtr(a, b *int) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

func NewDiscoverer(catalog *LimitsCatalog) DiscoverFunc {
	return func(
		ctx context.Context,
		providerConfig modelstore.ModelProviderConfigRecord,
		apiKey string,
		allowLoopback bool,
	) ([]DiscoveredModel, error) {
		models, err := DiscoverModels(ctx, providerConfig, apiKey, allowLoopback)
		if err != nil {
			return nil, err
		}
		return catalog.FillMissingLimits(ctx, models), nil
	}
}
