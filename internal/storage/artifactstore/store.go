package artifactstore

import (
	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/dbconn"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/storeutil"
)

type ID = uuid.UUID

var NilID = uuid.Nil

type Store struct {
	db    dbconn.DB
	q     *dbsqlc.Queries
	blobs blobstore.Store
}

func New(db dbconn.DB, blobs blobstore.Store) *Store {
	return &Store{db: db, q: dbsqlc.New(db), blobs: blobs}
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
