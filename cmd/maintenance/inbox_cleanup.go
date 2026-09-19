package main

import (
	"context"
	"os"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/config"
	"github.com/omnara-ai/omnara/internal/integration"
	"github.com/omnara-ai/omnara/internal/storage"
)

// Blob configuration and network work happen only after discard has committed.
// A missing configuration is harmless for media-free plans; the artifact store
// reports a retryable cleanup warning only if there are actual prepared keys.
func cleanupDiscardedInbox(ctx context.Context, pool *pgxpool.Pool, projectID, receiptID uuid.UUID) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	store := storage.NewStore(pool)
	if os.Getenv("OMNARA_BLOB_S3_BUCKET") != "" {
		cfg, err := config.Load()
		if err != nil {
			return err
		}
		blobs, err := blobstore.NewS3Store(ctx, cfg.BlobStoreS3Config())
		if err != nil {
			return err
		}
		store = storage.NewStore(pool, storage.WithBlobStore(blobs))
	}
	return integration.CleanupDiscardedAppInboxArtifacts(
		ctx,
		store.Integrations(),
		store.Artifacts(),
		projectID,
		receiptID,
	)
}
