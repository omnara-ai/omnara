package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/hashicorp/golang-lru/v2/simplelru"
	"github.com/omnara-ai/omnara/internal/secrets"
	"github.com/omnara-ai/omnara/internal/storage"
)

const maxSigV4CredentialCacheEntries = 1_024

type SigV4CredentialCache struct {
	mu      sync.Mutex
	entries *simplelru.LRU[sigV4CredentialCacheKey, sigV4CredentialCacheEntry]
}

type sigV4CredentialCacheKey struct {
	secretID storage.ID
	region   string
}

type sigV4CredentialCacheEntry struct {
	versionID storage.ID
	provider  *aws.CredentialsCache
}

func NewSigV4CredentialCache() (*SigV4CredentialCache, error) {
	entries, err := simplelru.NewLRU[sigV4CredentialCacheKey, sigV4CredentialCacheEntry](
		maxSigV4CredentialCacheEntries,
		nil,
	)
	if err != nil {
		return nil, fmt.Errorf("create SigV4 credential cache: %w", err)
	}
	return &SigV4CredentialCache{entries: entries}, nil
}

func (c *SigV4CredentialCache) provider(
	secretID storage.ID,
	versionID storage.ID,
	region string,
	payload secrets.Payload,
) (aws.CredentialsProvider, error) {
	if c == nil {
		return nil, fmt.Errorf("sigv4 credential cache is required for AWS role assumption")
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	key := sigV4CredentialCacheKey{secretID: secretID, region: region}
	entry, ok := c.entries.Get(key)
	if ok && entry.versionID == versionID {
		return entry.provider, nil
	}
	staticProvider := staticAWSCredentialProvider(payload)
	cfg := aws.Config{Region: region, Credentials: staticProvider}
	provider := aws.NewCredentialsCache(
		newAssumeRoleCredentialProvider(
			sts.NewFromConfig(cfg),
			payload[secrets.KeyAWSRoleARN],
			payload[secrets.KeyAWSExternalID],
		),
		func(options *aws.CredentialsCacheOptions) {
			options.ExpiryWindow = 30 * time.Second
		},
	)
	c.entries.Add(key, sigV4CredentialCacheEntry{
		versionID: versionID,
		provider:  provider,
	})
	return provider, nil
}

func newSigV4RequestPreparer(
	service string,
	region string,
	provider aws.CredentialsProvider,
) (func(context.Context, *http.Request, []byte) error, error) {
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, fmt.Errorf("aws signing service is required")
	}
	region = strings.TrimSpace(region)
	if region == "" {
		return nil, fmt.Errorf("aws signing region is required")
	}
	if provider == nil {
		return nil, fmt.Errorf("aws credential provider is required")
	}
	signer := v4.NewSigner()
	return func(ctx context.Context, request *http.Request, body []byte) error {
		resolved, err := provider.Retrieve(ctx)
		if err != nil {
			return err
		}
		hash := sha256.Sum256(body)
		return signer.SignHTTP(
			ctx,
			resolved,
			request,
			hex.EncodeToString(hash[:]),
			service,
			region,
			time.Now(),
		)
	}, nil
}

func resolveAWSCredentialProvider(
	cache *SigV4CredentialCache,
	secretID storage.ID,
	versionID storage.ID,
	region string,
	payload secrets.Payload,
) (aws.CredentialsProvider, error) {
	roleARN := payload[secrets.KeyAWSRoleARN]
	if roleARN == "" {
		return staticAWSCredentialProvider(payload), nil
	}
	return cache.provider(secretID, versionID, region, payload)
}

func staticAWSCredentialProvider(payload secrets.Payload) aws.CredentialsProvider {
	return credentials.NewStaticCredentialsProvider(
		payload[secrets.KeyAWSAccessKeyID],
		payload[secrets.KeyAWSSecretAccessKey],
		payload[secrets.KeyAWSSessionToken],
	)
}

func newAssumeRoleCredentialProvider(
	client stscreds.AssumeRoleAPIClient,
	roleARN string,
	externalID string,
) aws.CredentialsProvider {
	return stscreds.NewAssumeRoleProvider(client, roleARN, func(options *stscreds.AssumeRoleOptions) {
		options.RoleSessionName = "omnara"
		if externalID != "" {
			options.ExternalID = aws.String(externalID)
		}
	})
}
