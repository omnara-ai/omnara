// Package blobstore defines the key-addressed blob storage contract and
// its S3 backend.
package blobstore

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

var ErrNotFound = errors.New("blob not found")

// ContentDigest is the canonical digest format for blob content; artifact
// idempotent-replay validation compares these digests.
func ContentDigest(content []byte) string {
	sum := sha256.Sum256(content)
	return "sha256:" + hex.EncodeToString(sum[:])
}

type Metadata struct {
	Digest    string
	SizeBytes int64
}

type Store interface {
	PutBlob(ctx context.Context, key string, content []byte) (Metadata, error)
	GetBlob(ctx context.Context, key string) ([]byte, Metadata, error)
	DeleteBlob(ctx context.Context, key string) error
}

// digestMetadataKey carries the sha256 digest as S3 object metadata so
// reads avoid rehashing.
const digestMetadataKey = "omnara-digest"

// S3Config configures the S3-backed blob store. Endpoint and static
// credentials are for S3-compatible servers; leave them empty on AWS.
type S3Config struct {
	Bucket          string
	Region          string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	// UsePathStyle addresses the bucket as a path segment instead of a
	// subdomain. Required by MinIO and most S3-compatible servers.
	UsePathStyle bool
}

type S3Store struct {
	client *s3.Client
	bucket string
}

var _ Store = (*S3Store)(nil)

func NewS3Store(ctx context.Context, cfg S3Config) (*S3Store, error) {
	if cfg.Bucket == "" {
		return nil, errors.New("blob store bucket is required")
	}
	loadOptions := []func(*awsconfig.LoadOptions) error{}
	if cfg.Region != "" {
		loadOptions = append(loadOptions, awsconfig.WithRegion(cfg.Region))
	}
	if cfg.AccessKeyID != "" || cfg.SecretAccessKey != "" {
		loadOptions = append(
			loadOptions,
			awsconfig.WithCredentialsProvider(
				credentials.NewStaticCredentialsProvider(cfg.AccessKeyID, cfg.SecretAccessKey, ""),
			),
		)
	}
	awsCfg, err := awsconfig.LoadDefaultConfig(ctx, loadOptions...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	client := s3.NewFromConfig(awsCfg, func(options *s3.Options) {
		if cfg.Endpoint != "" {
			options.BaseEndpoint = aws.String(cfg.Endpoint)
		}
		options.UsePathStyle = cfg.UsePathStyle
	})
	return &S3Store{client: client, bucket: cfg.Bucket}, nil
}

func (s *S3Store) PutBlob(ctx context.Context, key string, content []byte) (Metadata, error) {
	if key == "" {
		return Metadata{}, errors.New("blob key is required")
	}
	digest := ContentDigest(content)
	if _, err := s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:   aws.String(s.bucket),
		Key:      aws.String(key),
		Body:     bytes.NewReader(content),
		Metadata: map[string]string{digestMetadataKey: digest},
	}); err != nil {
		return Metadata{}, fmt.Errorf("put blob %q: %w", key, err)
	}
	return Metadata{Digest: digest, SizeBytes: int64(len(content))}, nil
}

func (s *S3Store) GetBlob(ctx context.Context, key string) ([]byte, Metadata, error) {
	output, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		var noSuchKey *types.NoSuchKey
		if errors.As(err, &noSuchKey) {
			return nil, Metadata{}, ErrNotFound
		}
		return nil, Metadata{}, fmt.Errorf("get blob %q: %w", key, err)
	}
	defer func() { _ = output.Body.Close() }()
	var buf bytes.Buffer
	if output.ContentLength != nil && *output.ContentLength > 0 {
		buf.Grow(int(*output.ContentLength))
	}
	if _, err := io.Copy(&buf, output.Body); err != nil {
		return nil, Metadata{}, fmt.Errorf("read blob %q: %w", key, err)
	}
	content := buf.Bytes()
	digest, ok := output.Metadata[digestMetadataKey]
	if !ok {
		return nil, Metadata{}, fmt.Errorf(
			"blob %q is missing the %s object metadata; it was not written by this store",
			key,
			digestMetadataKey,
		)
	}
	return content, Metadata{Digest: digest, SizeBytes: int64(len(content))}, nil
}

func (s *S3Store) DeleteBlob(ctx context.Context, key string) error {
	if _, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucket),
		Key:    aws.String(key),
	}); err != nil {
		return fmt.Errorf("delete blob %q: %w", key, err)
	}
	return nil
}
