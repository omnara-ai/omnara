package artifactstore

import (
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type Store struct {
	pool  *storeutil.Pool
	q     *dbsqlc.Queries
	blobs blobstore.Store
}

func New(pool *pgxpool.Pool, blobs blobstore.Store) *Store {
	db := storeutil.WrapPool(pool)
	return &Store{pool: db, q: dbsqlc.New(db), blobs: blobs}
}
