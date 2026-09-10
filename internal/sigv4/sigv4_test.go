package sigv4

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/secrets"
)

func TestSignerApply(t *testing.T) {
	signer, err := NewSigner(
		"bedrock-mantle",
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", "session-token"),
	)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	body := []byte(`{"model":"test"}`)
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://bedrock-mantle.us-west-2.api.aws/v1/chat/completions",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if err := signer.Apply(request); err != nil {
		t.Fatalf("sign request: %v", err)
	}
	authorization := request.Header.Get("Authorization")
	for _, want := range []string{
		"AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/",
		"/us-west-2/bedrock-mantle/aws4_request",
		"x-amz-content-sha256",
	} {
		if !strings.Contains(authorization, want) {
			t.Errorf("Authorization header %q does not contain %q", authorization, want)
		}
	}
	if request.Header.Get("X-Amz-Security-Token") != "session-token" {
		t.Errorf("X-Amz-Security-Token = %q", request.Header.Get("X-Amz-Security-Token"))
	}
	const expectedPayloadHash = "d50ed5ca99175e8e9191d49bd594c590623a297046d7cd095b6a7ee25734da06"
	if got := request.Header.Get("X-Amz-Content-Sha256"); got != expectedPayloadHash {
		t.Errorf("X-Amz-Content-Sha256 = %q, want %q", got, expectedPayloadHash)
	}
	if strings.Contains(authorization, "content-length") {
		t.Errorf("Authorization header signs content-length: %q", authorization)
	}
	gotBody, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read request body: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("signed body = %s, want %s", gotBody, body)
	}
}

func TestSignerApplyRejectsNonReplayableBody(t *testing.T) {
	signer, err := NewSigner(
		"bedrock-mantle",
		"us-west-2",
		credentials.NewStaticCredentialsProvider("AKIAEXAMPLE", "secret", ""),
	)
	if err != nil {
		t.Fatalf("create signer: %v", err)
	}
	request, err := http.NewRequestWithContext(
		context.Background(),
		http.MethodPost,
		"https://bedrock-mantle.us-west-2.api.aws/v1/chat/completions",
		io.NopCloser(strings.NewReader(`{"model":"test"}`)),
	)
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	if err := signer.Apply(request); err == nil || !strings.Contains(err.Error(), "must be replayable") {
		t.Fatalf("sign non-replayable request error = %v", err)
	}
}

func TestAssumeRoleCredentialProvider(t *testing.T) {
	expires := time.Now().Add(time.Hour)
	client := assumeRoleClientFunc(func(
		_ context.Context,
		input *sts.AssumeRoleInput,
		_ ...func(*sts.Options),
	) (*sts.AssumeRoleOutput, error) {
		if got := aws.ToString(input.RoleArn); got != "arn:aws:iam::123456789012:role/ReadOnly" {
			t.Fatalf("role ARN = %q", got)
		}
		if got := aws.ToString(input.RoleSessionName); got != "omnara" {
			t.Fatalf("role session name = %q", got)
		}
		if got := aws.ToString(input.ExternalId); got != "external" {
			t.Fatalf("external ID = %q", got)
		}
		return &sts.AssumeRoleOutput{Credentials: &types.Credentials{
			AccessKeyId:     aws.String("ASIAASSUMED"),
			SecretAccessKey: aws.String("assumed-secret"),
			SessionToken:    aws.String("assumed-session"),
			Expiration:      &expires,
		}}, nil
	})
	provider := newAssumeRoleCredentialProvider(
		client,
		"arn:aws:iam::123456789012:role/ReadOnly",
		"external",
	)
	resolved, err := provider.Retrieve(context.Background())
	if err != nil {
		t.Fatalf("assume role: %v", err)
	}
	if resolved.AccessKeyID != "ASIAASSUMED" ||
		resolved.SecretAccessKey != "assumed-secret" ||
		resolved.SessionToken != "assumed-session" ||
		!resolved.CanExpire ||
		!resolved.Expires.Equal(expires) {
		t.Fatalf("resolved credentials = %+v", resolved)
	}
}

func TestResolveCredentialProviderScopesSecretVersionByRegion(t *testing.T) {
	cache := newTestCredentialCache(t)
	secretID := uuid.New()
	versionID := uuid.New()
	payload := testAssumeRolePayload()
	first, err := ResolveCredentialProvider(cache, secretID, versionID, "us-west-2", payload)
	if err != nil {
		t.Fatalf("get first provider: %v", err)
	}
	sameRegion, err := ResolveCredentialProvider(cache, secretID, versionID, "us-west-2", payload)
	if err != nil {
		t.Fatalf("get same-region provider: %v", err)
	}
	if first != sameRegion {
		t.Fatal("same secret version and region returned different providers")
	}
	differentRegion, err := ResolveCredentialProvider(cache, secretID, versionID, "us-east-1", payload)
	if err != nil {
		t.Fatalf("get different-region provider: %v", err)
	}
	if first == differentRegion {
		t.Fatal("same secret version in different regions returned the same provider")
	}
}

func TestResolveCredentialProviderRequiresCacheForAssumeRole(t *testing.T) {
	_, err := ResolveCredentialProvider(nil, uuid.New(), uuid.New(), "us-west-2", testAssumeRolePayload())
	if err == nil || !strings.Contains(err.Error(), "credential cache is required") {
		t.Fatalf("resolve role provider error = %v, want missing credential cache", err)
	}
}

func TestResolveCredentialProviderReplacesRotatedSecret(t *testing.T) {
	cache := newTestCredentialCache(t)
	secretID := uuid.New()
	payload := testAssumeRolePayload()
	first, err := ResolveCredentialProvider(cache, secretID, uuid.New(), "us-west-2", payload)
	if err != nil {
		t.Fatalf("get first provider: %v", err)
	}
	second, err := ResolveCredentialProvider(cache, secretID, uuid.New(), "us-west-2", payload)
	if err != nil {
		t.Fatalf("get rotated provider: %v", err)
	}
	if first == second {
		t.Fatal("rotated secret version reused the previous provider")
	}
}

func TestResolveCredentialProviderEvictsLeastRecentlyUsed(t *testing.T) {
	cache := newTestCredentialCache(t)
	payload := testAssumeRolePayload()
	resolve := func(secretID, versionID uuid.UUID) aws.CredentialsProvider {
		t.Helper()
		provider, err := ResolveCredentialProvider(cache, secretID, versionID, "us-west-2", payload)
		if err != nil {
			t.Fatalf("resolve provider: %v", err)
		}
		return provider
	}
	firstSecretID, firstVersionID := uuid.New(), uuid.New()
	first := resolve(firstSecretID, firstVersionID)
	secondSecretID, secondVersionID := uuid.New(), uuid.New()
	second := resolve(secondSecretID, secondVersionID)
	for range maxCredentialCacheEntries - 2 {
		resolve(uuid.New(), uuid.New())
	}
	resolve(firstSecretID, firstVersionID)
	resolve(uuid.New(), uuid.New())
	if got := resolve(firstSecretID, firstVersionID); got != first {
		t.Fatal("recently used provider was evicted")
	}
	if got := resolve(secondSecretID, secondVersionID); got == second {
		t.Fatal("least recently used provider was not evicted")
	}
	if got := cache.entries.Len(); got != maxCredentialCacheEntries {
		t.Fatalf("cache entries = %d, want %d", got, maxCredentialCacheEntries)
	}
}

func newTestCredentialCache(t *testing.T) *CredentialCache {
	t.Helper()
	cache, err := NewCredentialCache()
	if err != nil {
		t.Fatalf("create SigV4 credential cache: %v", err)
	}
	return cache
}

func testAssumeRolePayload() secrets.Payload {
	return secrets.Payload{
		secrets.KeyAWSAccessKeyID:     "AKIAEXAMPLE",
		secrets.KeyAWSSecretAccessKey: "secret",
		secrets.KeyAWSRoleARN:         "arn:aws:iam::123456789012:role/ReadOnly",
		secrets.KeyAWSExternalID:      "external",
	}
}

type assumeRoleClientFunc func(
	context.Context,
	*sts.AssumeRoleInput,
	...func(*sts.Options),
) (*sts.AssumeRoleOutput, error)

func (f assumeRoleClientFunc) AssumeRole(
	ctx context.Context,
	input *sts.AssumeRoleInput,
	options ...func(*sts.Options),
) (*sts.AssumeRoleOutput, error) {
	return f(ctx, input, options...)
}
