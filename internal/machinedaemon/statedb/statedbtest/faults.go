package statedbtest

import (
	"context"
	"database/sql"
	"errors"

	_ "modernc.org/sqlite"
)

func SetProcessDeleteFailure(
	ctx context.Context,
	path string,
	enabled bool,
) error {
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return err
	}
	statement := `DROP TRIGGER IF EXISTS fail_rejected_preparation_delete`
	if enabled {
		statement = `
CREATE TRIGGER fail_rejected_preparation_delete
BEFORE DELETE ON processes
BEGIN
    SELECT RAISE(ABORT, 'injected rejected preparation delete failure');
END;
`
	}
	_, execErr := db.ExecContext(ctx, statement)
	return errors.Join(execErr, db.Close())
}
