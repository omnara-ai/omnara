package artifactstore

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/storage/dbconn"
)

type artifactBeginFailureDB struct {
	dbconn.DB
	err error
}

func (db artifactBeginFailureDB) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, db.err
}

type cancelingArtifactBlobStore struct {
	blobstore.Store
	cancel       context.CancelFunc
	deleteCalled bool
	deleteCtxErr error
}

func (store *cancelingArtifactBlobStore) PutBlob(
	context.Context,
	string,
	[]byte,
) (blobstore.Metadata, error) {
	store.cancel()
	return blobstore.Metadata{Digest: "sha256:test", SizeBytes: 4}, nil
}

func (store *cancelingArtifactBlobStore) DeleteBlob(ctx context.Context, _ string) error {
	store.deleteCalled = true
	store.deleteCtxErr = ctx.Err()
	return store.deleteCtxErr
}

func TestArtifactObjectKeyUsesAgentAndArtifactID(t *testing.T) {
	agentID := parseUUIDText("018ffc6b-7f1a-7828-8687-93aa210f5f4a")
	artifactID := parseUUIDText("018ffc6b-7f1a-7c16-8a7a-973be2463b7d")

	if got, want := artifactObjectKey(
		agentID,
		artifactID,
	), "artifacts/018ffc6b-7f1a-7828-8687-93aa210f5f4a/018ffc6b-7f1a-7c16-8a7a-973be2463b7d"; got != want {
		t.Fatalf("artifact object key = %q, want %q", got, want)
	}
}

func TestCreateArtifactCleanupSurvivesCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	beginErr := errors.New("begin transaction")
	blobs := &cancelingArtifactBlobStore{cancel: cancel}
	store := New(artifactBeginFailureDB{err: beginErr}, blobs)

	_, err := store.CreateArtifact(ctx, CreateArtifactInput{
		ProjectID:   parseUUIDText("018ffc6b-7f1a-7828-8687-93aa210f5f4a"),
		AgentID:     parseUUIDText("018ffc6b-7f1a-7c16-8a7a-973be2463b7d"),
		ContentType: "text/plain",
		Content:     []byte("test"),
	})
	if !errors.Is(err, beginErr) {
		t.Fatalf("create artifact error = %v, want %v", err, beginErr)
	}
	if !blobs.deleteCalled {
		t.Fatal("uploaded blob was not cleaned up")
	}
	if blobs.deleteCtxErr != nil {
		t.Fatalf("cleanup context error = %v, want nil", blobs.deleteCtxErr)
	}
}
