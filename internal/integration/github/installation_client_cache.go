package github

import (
	"errors"
	"net/http"
	"sync"

	"github.com/google/uuid"
	"github.com/hashicorp/golang-lru/v2/simplelru"
)

type InstallationClientCacheConfig struct {
	Capacity   int
	HTTPClient *http.Client
	APIURL     string
}

type InstallationClientCache struct {
	mu      sync.Mutex
	clients *simplelru.LRU[installationClientKey, *Client]
	config  InstallationClientCacheConfig
}

type installationClientKey struct {
	secretID       uuid.UUID
	versionID      uuid.UUID
	installationID int64
	appID          int64
}

func NewInstallationClientCache(config InstallationClientCacheConfig) (*InstallationClientCache, error) {
	clients, err := simplelru.NewLRU[installationClientKey, *Client](config.Capacity, nil)
	if err != nil {
		return nil, err
	}
	return &InstallationClientCache{clients: clients, config: config}, nil
}

func (c *InstallationClientCache) Client(
	secretID, versionID uuid.UUID,
	installationID int64,
	credentials Credentials,
) (*Client, error) {
	if secretID == uuid.Nil || versionID == uuid.Nil || installationID <= 0 || credentials.AppID <= 0 {
		return nil, errors.New("github client cache requires credential secret/version, installation and App IDs")
	}
	key := installationClientKey{
		secretID: secretID, versionID: versionID, installationID: installationID, appID: credentials.AppID,
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if client, ok := c.clients.Get(key); ok {
		return client, nil
	}
	client, err := NewClient(Config{
		Credentials: credentials, InstallationID: installationID,
		HTTPClient: c.config.HTTPClient, APIURL: c.config.APIURL,
	})
	if err != nil {
		return nil, err
	}
	c.clients.Add(key, client)
	return client, nil
}
