package mcp

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/aws/aws-sdk-go-v2/service/sts/types"
	"github.com/omnara-ai/omnara/internal/secrets"
)

func TestBuildHTTPPreparesSigV4Request(t *testing.T) {
	ctx := context.Background()
	prepareRequest, err := newSigV4RequestPreparer(
		ctx,
		"execute-api",
		"us-west-2",
		secrets.Payload{
			secrets.KeyAWSAccessKeyID:     "AKIAEXAMPLE",
			secrets.KeyAWSSecretAccessKey: "secret",
			secrets.KeyAWSSessionToken:    "session-token",
		},
	)
	if err != nil {
		t.Fatalf("create SigV4 request signer: %v", err)
	}
	body := []byte(`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}`)
	request, err := (&httpClient{}).buildHTTP(ctx, Conn{
		EndpointURL:     "https://example.execute-api.us-west-2.amazonaws.com/mcp",
		MCPSessionID:    "session",
		ProtocolVersion: ProtocolVersion,
		prepareRequest:  prepareRequest,
	}, body)
	if err != nil {
		t.Fatalf("build signed request: %v", err)
	}
	authorization := request.Header.Get("Authorization")
	for _, want := range []string{
		"AWS4-HMAC-SHA256 Credential=AKIAEXAMPLE/",
		"/us-west-2/execute-api/aws4_request",
		"mcp-protocol-version",
		"mcp-session-id",
	} {
		if !strings.Contains(authorization, want) {
			t.Errorf("Authorization header %q does not contain %q", authorization, want)
		}
	}
	if request.Header.Get("X-Amz-Date") == "" {
		t.Error("signed request is missing X-Amz-Date")
	}
	if request.Header.Get("X-Amz-Security-Token") != "session-token" {
		t.Errorf("X-Amz-Security-Token = %q", request.Header.Get("X-Amz-Security-Token"))
	}
	gotBody, err := io.ReadAll(request.Body)
	if err != nil {
		t.Fatalf("read signed request body: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Fatalf("signed body = %s, want %s", gotBody, body)
	}
}

func TestAssumeRoleCredentials(t *testing.T) {
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
	resolved, err := assumeRoleCredentials(
		context.Background(),
		client,
		"arn:aws:iam::123456789012:role/ReadOnly",
		"external",
	)
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
