package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/simplelru"
)

const (
	gitCredentialExpiryMargin        = 15 * time.Minute
	gitCredentialCacheSize           = 1024
	InstallationRepositoriesPageSize = 100
)

var ErrContentsPermissionRequired = errors.New(
	"github installation requires Contents read or write permission for Git credentials; " +
		"update the GitHub App permissions and approve them for this installation",
)

type InstallationCredentials struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

var sharedGitCredentials = sync.OnceValues(newGitCredentialCache)

type gitCredentialKey struct {
	installationTokenIdentity
	scope string
}

type gitCredentialEntry struct {
	gate  chan struct{}
	value InstallationCredentials
}

type gitCredentialCache struct {
	mu      sync.Mutex
	entries *simplelru.LRU[gitCredentialKey, *gitCredentialEntry]
}

func newGitCredentialCache() (*gitCredentialCache, error) {
	entries, err := simplelru.NewLRU[gitCredentialKey, *gitCredentialEntry](gitCredentialCacheSize, nil)
	if err != nil {
		return nil, err
	}
	return &gitCredentialCache{entries: entries}, nil
}

func (c *gitCredentialCache) entry(key gitCredentialKey) *gitCredentialEntry {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry, found := c.entries.Get(key)
	if !found {
		entry = &gitCredentialEntry{gate: make(chan struct{}, 1)}
		c.entries.Add(key, entry)
	}
	return entry
}

func (c *gitCredentialCache) invalidate(token string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, key := range c.entries.Keys() {
		if entry, _ := c.entries.Peek(key); entry.value.Token == token {
			entry.value = InstallationCredentials{}
		}
	}
}

// InstallationGitCredentials requires caller-owned live authorization checks
// before and after every call, including cache hits, when the caller hands out
// the credentials.
func (c *Client) InstallationGitCredentials(
	ctx context.Context, write bool, repositoryIDs []int64,
) (InstallationCredentials, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	permission := "read"
	if write {
		permission = "write"
	}
	scope := permission
	for _, id := range repositoryIDs {
		scope += "/" + strconv.FormatInt(id, 10)
	}
	entry := c.gitCredentials.entry(gitCredentialKey{installationTokenIdentity: c.tokenIdentity, scope: scope})
	select {
	case entry.gate <- struct{}{}:
		defer func() { <-entry.gate }()
	case <-ctx.Done():
		return InstallationCredentials{}, ctx.Err()
	}
	if err := ctx.Err(); err != nil {
		return InstallationCredentials{}, err
	}
	c.gitCredentials.mu.Lock()
	cached := entry.value
	c.gitCredentials.mu.Unlock()
	if cached.ExpiresAt.After(c.now().Add(gitCredentialExpiryMargin)) {
		return cached, nil
	}
	jwt, err := c.appJWT()
	if err != nil {
		return InstallationCredentials{}, err
	}
	var result struct {
		InstallationCredentials
		Permissions map[string]string `json:"permissions"`
	}
	input := struct {
		Permissions   map[string]string `json:"permissions"`
		RepositoryIDs []int64           `json:"repository_ids,omitempty"`
	}{map[string]string{"contents": permission, "metadata": "read"}, repositoryIDs}
	path := "/app/installations/" + strconv.FormatInt(c.installationID, 10) + "/access_tokens"
	if _, err := c.doJSON(ctx, http.MethodPost, path, jwt, input, &result, false); err != nil {
		return InstallationCredentials{}, err
	}
	if err := ctx.Err(); err != nil {
		return InstallationCredentials{}, err
	}
	if result.Token == "" || !result.ExpiresAt.After(c.now().Add(gitCredentialExpiryMargin)) {
		return InstallationCredentials{}, &APIError{Code: InvalidResponse}
	}
	for _, ch := range result.Token {
		if ch <= ' ' || ch > '~' {
			return InstallationCredentials{}, &APIError{Code: InvalidResponse}
		}
	}
	if result.Permissions["contents"] != "read" && result.Permissions["contents"] != "write" {
		_, _, _ = c.do(ctx, http.MethodDelete, "/installation/token", result.Token, jsonMediaType, nil, true)
		return InstallationCredentials{}, ErrContentsPermissionRequired
	}
	c.gitCredentials.mu.Lock()
	entry.value = result.InstallationCredentials
	c.gitCredentials.mu.Unlock()
	return result.InstallationCredentials, nil
}

func (c *Client) InstallationRepositories(
	ctx context.Context, write bool, repositoryIDs []int64, page int,
) ([]Repository, int, error) {
	if page < 1 {
		return nil, 0, errors.New("github repository page must be positive")
	}
	credentials, err := c.InstallationGitCredentials(ctx, write, repositoryIDs)
	if err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	var listing struct {
		Repositories []Repository `json:"repositories"`
	}
	path := "/installation/repositories?per_page=" + strconv.Itoa(InstallationRepositoriesPageSize) +
		"&page=" + strconv.Itoa(page)
	header, err := c.request(ctx, credentials.Token, http.MethodGet, path, nil, &listing)
	if err != nil {
		return nil, 0, err
	}
	if listing.Repositories == nil || len(listing.Repositories) > InstallationRepositoriesPageSize {
		return nil, 0, &APIError{Code: InvalidResponse}
	}
	for _, repository := range listing.Repositories {
		if repository.ID <= 0 || !validRepositorySegment(repository.Name) {
			return nil, 0, &APIError{Code: InvalidResponse}
		}
	}
	next, err := c.nextPage(header.Values("Link"), PageOptions{Page: page, PerPage: InstallationRepositoriesPageSize},
		"/installation/repositories")
	if err != nil {
		return nil, 0, err
	}
	return listing.Repositories, next, nil
}
