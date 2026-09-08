package modelprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/simplelru"
	"github.com/omnara-ai/omnara/internal/model/anthropicmessages"
	"github.com/omnara-ai/omnara/internal/model/route"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

const (
	messagesFallbackOutputTokens = 64_000
	messagesLimitLookupTimeout   = 3 * time.Second
	messagesLimitCacheTTL        = time.Hour
	messagesLimitFailureTTL      = time.Minute
	messagesLimitCacheSize       = 1_024
)

// MessagesOutputLimits caches provider output limits. Its zero value is ready for use.
// A nil cache performs uncached lookups.
type MessagesOutputLimits struct {
	mu       sync.Mutex
	entries  *simplelru.LRU[messagesOutputLimitKey, messagesOutputLimitEntry]
	inflight map[messagesOutputLimitKey]chan struct{}
}

type messagesOutputLimitKey struct {
	providerID        modelstore.ID
	providerUpdatedAt int64
	credentialVersion modelstore.ID
	slug              string
}

type messagesOutputLimitEntry struct {
	tokens    int
	expiresAt time.Time
}

func (r Resolver) messagesOutputAllowance(
	ctx context.Context,
	provider modelstore.ModelProviderConfigRecord,
	credentialVersion modelstore.ID,
	slug string,
	auth route.Auth,
) (int, error) {
	if provider.APIVariant == modelprotocol.APIVariantBedrock {
		return messagesFallbackOutputTokens, nil
	}
	key := messagesOutputLimitKey{
		providerID: provider.ID, providerUpdatedAt: provider.UpdatedAt.UnixNano(),
		credentialVersion: credentialVersion, slug: slug,
	}
	limit, err := r.MessagesOutputLimits.lookup(ctx, key, func(ctx context.Context) (int, error) {
		return fetchMessagesOutputLimit(ctx, provider.BaseURL, slug, auth, newSSRFHTTPClient(r.AllowLoopback))
	})
	if err != nil {
		return 0, err
	}
	if limit == 0 {
		limit = messagesFallbackOutputTokens
	}
	return limit, nil
}

func (c *MessagesOutputLimits) lookup(
	ctx context.Context,
	key messagesOutputLimitKey,
	fetch func(context.Context) (int, error),
) (int, error) {
	for {
		if err := ctx.Err(); err != nil {
			return 0, err
		}
		if c == nil {
			limit, err := fetch(ctx)
			if err != nil {
				limit = 0
			}
			return limit, ctx.Err()
		}
		c.mu.Lock()
		if c.entries == nil {
			// The fixed positive capacity cannot fail LRU construction.
			c.entries, _ = simplelru.NewLRU[messagesOutputLimitKey, messagesOutputLimitEntry](messagesLimitCacheSize, nil)
			c.inflight = make(map[messagesOutputLimitKey]chan struct{})
		}
		if entry, found := c.entries.Get(key); found && time.Now().Before(entry.expiresAt) {
			c.mu.Unlock()
			return entry.tokens, nil
		}
		if done, waiting := c.inflight[key]; waiting {
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return 0, ctx.Err()
			case <-done:
				continue
			}
		}
		done := make(chan struct{})
		c.inflight[key] = done
		c.mu.Unlock()
		defer func() {
			c.mu.Lock()
			delete(c.inflight, key)
			close(done)
			c.mu.Unlock()
		}()

		limit, err := fetch(ctx)
		if err != nil {
			limit = 0
		}
		c.mu.Lock()
		if ctx.Err() == nil {
			ttl := messagesLimitCacheTTL
			if limit == 0 {
				ttl = messagesLimitFailureTTL
			}
			c.entries.Add(key, messagesOutputLimitEntry{tokens: limit, expiresAt: time.Now().Add(ttl)})
		}
		c.mu.Unlock()
		return limit, ctx.Err()
	}
}

func fetchMessagesOutputLimit(
	ctx context.Context, baseURL, slug string, auth route.Auth, client *http.Client,
) (int, error) {
	ctx, cancel := context.WithTimeout(ctx, messagesLimitLookupTimeout)
	defer cancel()
	body, err := getDiscoveryEndpoint(
		ctx, outboundhttp.CloneWithoutRedirects(client), baseURL,
		"/models/"+url.PathEscape(slug), "Messages model metadata endpoint",
		route.Chain{auth, route.Headers{"Anthropic-Version": anthropicmessages.APIVersion}}, discoveryMaxResponseSize,
	)
	if err != nil {
		return 0, err
	}
	var info struct {
		MaxTokens json.RawMessage `json:"max_tokens"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		return 0, err
	}
	if limit := modelTokenCount(info.MaxTokens); limit != nil {
		return *limit, nil
	}
	return 0, nil
}
