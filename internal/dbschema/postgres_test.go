package dbschema

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
)

type stubQueryRower struct {
	version int64
	err     error
	query   string
}

func (s *stubQueryRower) QueryRow(_ context.Context, query string, _ ...any) pgx.Row {
	s.query = query
	return stubRow{version: s.version, err: s.err}
}

type stubRow struct {
	version int64
	err     error
}

func (r stubRow) Scan(dest ...any) error {
	if r.err != nil {
		return r.err
	}
	version, ok := dest[0].(*int64)
	if !ok {
		return errors.New("stub row destination is not *int64")
	}
	*version = r.version
	return nil
}

func TestRequireVersion(t *testing.T) {
	for _, test := range []struct {
		name    string
		current int64
		wantErr string
	}{
		{name: "older", current: 15, wantErr: "version 15 is older than required minimum 16"},
		{name: "minimum", current: 16},
		{name: "newer additive schema", current: 17},
	} {
		t.Run(test.name, func(t *testing.T) {
			db := &stubQueryRower{version: test.current}
			err := RequireVersion(context.Background(), db, 16)
			if test.wantErr == "" && err != nil {
				t.Fatalf("require schema version: %v", err)
			}
			if test.wantErr != "" && (err == nil || !strings.Contains(err.Error(), test.wantErr)) {
				t.Fatalf("schema error = %v, want substring %q", err, test.wantErr)
			}
			if !strings.Contains(db.query, "goose_db_version") {
				t.Fatalf("schema query = %q", db.query)
			}
		})
	}
}

func TestRequireVersionExplainsMissingVersionTable(t *testing.T) {
	db := &stubQueryRower{err: errors.New(`relation "goose_db_version" does not exist`)}
	err := RequireVersion(context.Background(), db, 16)
	if err == nil || !strings.Contains(err.Error(), "run omnara-migrate") {
		t.Fatalf("schema error = %v", err)
	}
}

func TestRequireVersionRejectsInvalidMinimum(t *testing.T) {
	err := RequireVersion(context.Background(), &stubQueryRower{}, 0)
	if err == nil || !strings.Contains(err.Error(), "must be positive") {
		t.Fatalf("minimum version error = %v", err)
	}
}
