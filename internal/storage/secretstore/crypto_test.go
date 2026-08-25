package secretstore

import (
	"context"
	"errors"
	"testing"

	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

func TestInvalidSecretNameIsFieldSpecificWithoutOpaqueRequestTag(t *testing.T) {
	err := invalidSecretName("secret name must not start or end with whitespace")
	if !errors.Is(err, storeerr.ErrInvalidSecretName) {
		t.Fatalf("error = %v, want ErrInvalidSecretName", err)
	}
	if !errors.Is(err, storeerr.ErrInvalidRequest) {
		t.Fatalf("error = %v, want ErrInvalidRequest", err)
	}
	if errors.Is(err, storeerr.ErrInvalidSecretRequest) {
		t.Fatalf("error = %v, must not carry opaque ErrInvalidSecretRequest tag", err)
	}
	if got, want := err.Error(), "secret name must not start or end with whitespace"; got != want {
		t.Fatalf("error = %q, want %q", got, want)
	}
}

func TestCreateTxRejectsInvalidNameBeforeDatabaseWrite(t *testing.T) {
	store := &Store{q: dbsqlc.New(nil)}
	_, _, err := store.CreateTx(
		context.Background(),
		nil,
		CreateSecretInput{Name: "unsafe\u200dname"},
	)
	if !errors.Is(err, storeerr.ErrInvalidSecretName) {
		t.Fatalf("error = %v, want ErrInvalidSecretName", err)
	}
}
