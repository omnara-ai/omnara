package github

import (
	"context"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/hashicorp/golang-lru/v2/simplelru"
)

var sharedInstallationTokens = sync.OnceValues(func() (*installationTokenCache, error) {
	return newInstallationTokenCache(1024)
})

type installationTokenIdentity struct {
	origin         string
	secretID       uuid.UUID
	versionID      uuid.UUID
	appID          int64
	installationID int64
}

type installationTokenKey struct {
	installationTokenIdentity
	repositoryID int64
	write        bool
}

type installationTokenEntry struct {
	gate  chan struct{}
	value cachedToken // Protected by installationTokenCache.mu, including invalidation.
}

type installationTokenCache struct {
	mu      sync.Mutex
	entries *simplelru.LRU[installationTokenKey, *installationTokenEntry]
}

func newInstallationTokenCache(capacity int) (*installationTokenCache, error) {
	entries, err := simplelru.NewLRU[installationTokenKey, *installationTokenEntry](capacity, nil)
	if err != nil {
		return nil, err
	}
	return &installationTokenCache{entries: entries}, nil
}

func (c *installationTokenCache) token(
	ctx context.Context, client *Client, repositoryID int64, write bool,
) (string, error) {
	key := installationTokenKey{installationTokenIdentity: client.tokenIdentity, repositoryID: repositoryID, write: write}
	c.mu.Lock()
	entry, found := c.entries.Get(key)
	if !found {
		entry = &installationTokenEntry{gate: make(chan struct{}, 1)}
		c.entries.Add(key, entry)
	}
	c.mu.Unlock()
	select {
	case entry.gate <- struct{}{}:
		defer func() { <-entry.gate }()
	case <-ctx.Done():
		return "", ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	cached := entry.value
	c.mu.Unlock()
	if cached.expiresAt.After(client.now().Add(time.Minute)) {
		return cached.token, nil
	}
	fresh, err := client.mintInstallationToken(ctx, repositoryID, write)
	if err != nil {
		return "", err
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	c.mu.Lock()
	entry.value = fresh
	c.mu.Unlock()
	return fresh.token, nil
}

func (c *installationTokenCache) invalidate(identity installationTokenIdentity, token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range c.entries.Keys() {
		if key.installationTokenIdentity != identity {
			continue
		}
		entry, _ := c.entries.Peek(key)
		if entry.value.token == token {
			entry.value = cachedToken{}
		}
	}
}
