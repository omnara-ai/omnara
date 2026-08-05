package artifactstore

import (
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type ID = uuid.UUID

var NilID = uuid.Nil

type Store struct {
	pool  *pgxpool.Pool
	q     *dbsqlc.Queries
	blobs blobstore.Store
}

func New(pool *pgxpool.Pool, blobs blobstore.Store) *Store {
	return &Store{pool: pool, q: dbsqlc.New(pool), blobs: blobs}
}

func isNilID(id ID) bool {
	return id == NilID
}

func parseUUIDText(value string) ID {
	id, err := uuid.Parse(value)
	if err != nil {
		return NilID
	}
	return id
}

func sqlcTextFromEmpty(value string) *string {
	return storeutil.TextFromEmpty(value)
}
