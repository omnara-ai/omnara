package migrations

import (
	"context"
	"database/sql"

	"github.com/pressly/goose/v3"
)

func newStorageToolRenameMigration() *goose.Migration {
	return goose.NewGoMigration(55, &goose.GoFunc{RunTx: upStorageToolRename}, nil)
}

var storageToolRenames = [][2]string{
	{"read_file", "storage_read"},
	{"write_file", "storage_write"},
	{"list_files", "storage_list"},
	{"search_files", "storage_search"},
	{"upload_file", "storage_upload"},
	{"download_file", "storage_download"},
}

func upStorageToolRename(ctx context.Context, tx *sql.Tx) error {
	return renameConfigTools(ctx, tx, storageToolRenames)
}
