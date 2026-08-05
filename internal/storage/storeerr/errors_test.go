package storeerr

import (
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
)

func TestTagKeepsMessageAndSentinel(t *testing.T) {
	base := errors.New("machine_sources[0] is invalid")
	err := InvalidRequest(base)
	if err.Error() != base.Error() {
		t.Fatalf("InvalidRequest message = %q, want %q", err.Error(), base.Error())
	}
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("InvalidRequest(%v) does not match ErrInvalidRequest", base)
	}
	if !errors.Is(err, base) {
		t.Fatalf("InvalidRequest(%v) does not match the wrapped error", base)
	}
	if InvalidRequest(nil) != nil {
		t.Fatal("InvalidRequest(nil) should be nil")
	}
}

func TestIsNotFound(t *testing.T) {
	for _, err := range []error{
		ErrNotFound,
		pgx.ErrNoRows,
		Tag(ErrNotFound, errors.New("missing artifact")),
	} {
		if !IsNotFound(err) {
			t.Fatalf("IsNotFound(%v) = false", err)
		}
	}
	if IsNotFound(errors.New("database unavailable")) {
		t.Fatal("unrelated error matched not found")
	}
}
