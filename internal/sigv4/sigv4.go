package sigv4

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
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

const maxCredentialCacheEntries = 1_024

type CredentialCache struct {
	mu      sync.Mutex
	entries *simplelru.LRU[credentialCacheKey, credentialCacheEntry]
}

type credentialCacheKey struct {
	secretID storage.ID
	region   string
}

type credentialCacheEntry struct {
	versionID storage.ID
	provider  *aws.CredentialsCache
}

func NewCredentialCache() (*CredentialCache, error) {
	entries, err := simplelru.NewLRU[credentialCacheKey, credentialCacheEntry](maxCredentialCacheEntries, nil)
	if err != nil {
		return nil, fmt.Errorf("create SigV4 credential cache: %w", err)
	}
	return &CredentialCache{entries: entries}, nil
}

func (c *CredentialCache) provider(
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
	key := credentialCacheKey{secretID: secretID, region: region}
	entry, ok := c.entries.Get(key)
	if ok && entry.versionID == versionID {
		return entry.provider, nil
	}
	staticProvider := staticCredentialProvider(payload)
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
	c.entries.Add(key, credentialCacheEntry{versionID: versionID, provider: provider})
	return provider, nil
}

type Signer struct {
	service  string
	region   string
	provider aws.CredentialsProvider
	signer   *v4.Signer
}

func NewSigner(service, region string, provider aws.CredentialsProvider) (*Signer, error) {
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
	return &Signer{service: service, region: region, provider: provider, signer: v4.NewSigner()}, nil
}

func (s *Signer) Sign(ctx context.Context, request *http.Request, body []byte) error {
	hash := sha256.Sum256(body)
	return s.sign(ctx, request, hex.EncodeToString(hash[:]))
}

func (s *Signer) Apply(request *http.Request) error {
	hash := sha256.New()
	if request.Body != nil && request.Body != http.NoBody {
		if request.GetBody == nil {
			return fmt.Errorf("sigv4 request body must be replayable")
		}
		body, err := request.GetBody()
		if err != nil {
			return fmt.Errorf("replay sigv4 request body: %w", err)
		}
		defer func() { _ = body.Close() }()
		if _, err := io.Copy(hash, body); err != nil {
			return fmt.Errorf("hash sigv4 request body: %w", err)
		}
	}
	payloadHash := hex.EncodeToString(hash.Sum(nil))
	request.Header.Del("Authorization")
	request.Header.Del("X-Amz-Date")
	request.Header.Del("X-Amz-Security-Token")
	request.Header.Set("X-Amz-Content-Sha256", payloadHash)
	contentLength := request.ContentLength
	request.ContentLength = -1
	err := s.sign(request.Context(), request, payloadHash)
	request.ContentLength = contentLength
	return err
}

func (s *Signer) sign(ctx context.Context, request *http.Request, payloadHash string) error {
	resolved, err := s.provider.Retrieve(ctx)
	if err != nil {
		return err
	}
	return s.signer.SignHTTP(
		ctx,
		resolved,
		request,
		payloadHash,
		s.service,
		s.region,
		time.Now(),
	)
}

func ResolveCredentialProvider(
	cache *CredentialCache,
	secretID storage.ID,
	versionID storage.ID,
	region string,
	payload secrets.Payload,
) (aws.CredentialsProvider, error) {
	if payload[secrets.KeyAWSRoleARN] == "" {
		return staticCredentialProvider(payload), nil
	}
	return cache.provider(secretID, versionID, region, payload)
}

func staticCredentialProvider(payload secrets.Payload) aws.CredentialsProvider {
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
