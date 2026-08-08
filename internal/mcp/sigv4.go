package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/credentials/stscreds"
	"github.com/aws/aws-sdk-go-v2/service/sts"
	"github.com/omnara-ai/omnara/internal/secrets"
)

func newSigV4RequestPreparer(
	ctx context.Context,
	service string,
	region string,
	payload secrets.Payload,
) (func(context.Context, *http.Request, []byte) error, error) {
	service = strings.TrimSpace(service)
	if service == "" {
		return nil, fmt.Errorf("aws signing service is required")
	}
	region = strings.TrimSpace(region)
	if region == "" {
		return nil, fmt.Errorf("aws signing region is required")
	}
	resolved, err := resolveAWSCredentials(ctx, region, payload)
	if err != nil {
		return nil, err
	}
	signer := v4.NewSigner()
	return func(ctx context.Context, request *http.Request, body []byte) error {
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

func resolveAWSCredentials(
	ctx context.Context,
	region string,
	payload secrets.Payload,
) (aws.Credentials, error) {
	resolved := aws.Credentials{
		AccessKeyID:     payload[secrets.KeyAWSAccessKeyID],
		SecretAccessKey: payload[secrets.KeyAWSSecretAccessKey],
		SessionToken:    payload[secrets.KeyAWSSessionToken],
	}
	roleARN := payload[secrets.KeyAWSRoleARN]
	if roleARN == "" {
		return resolved, nil
	}
	cfg := aws.Config{
		Region: region,
		Credentials: credentials.NewStaticCredentialsProvider(
			resolved.AccessKeyID,
			resolved.SecretAccessKey,
			resolved.SessionToken,
		),
	}
	return assumeRoleCredentials(
		ctx,
		sts.NewFromConfig(cfg),
		roleARN,
		payload[secrets.KeyAWSExternalID],
	)
}

func assumeRoleCredentials(
	ctx context.Context,
	client stscreds.AssumeRoleAPIClient,
	roleARN string,
	externalID string,
) (aws.Credentials, error) {
	provider := stscreds.NewAssumeRoleProvider(client, roleARN, func(options *stscreds.AssumeRoleOptions) {
		options.RoleSessionName = "omnara"
		if externalID != "" {
			options.ExternalID = aws.String(externalID)
		}
	})
	resolved, err := provider.Retrieve(ctx)
	if err != nil {
		return aws.Credentials{}, fmt.Errorf("assume AWS role: %w", err)
	}
	return resolved, nil
}
