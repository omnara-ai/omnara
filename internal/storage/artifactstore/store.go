package artifactstore

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

type Store struct {
	pool  *pgxpool.Pool
	q     *dbsqlc.Queries
	blobs blobstore.Store
}

func New(pool *pgxpool.Pool, blobs blobstore.Store) *Store {
	return &Store{pool: pool, q: dbsqlc.New(pool), blobs: blobs}
}
