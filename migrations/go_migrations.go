package migrations

import (
	"embed"

	"github.com/pressly/goose/v3"
)

//go:embed *.sql
var Files embed.FS

func GoMigrations() []*goose.Migration {
	return []*goose.Migration{
		newAgentConfigNameMigration(),
	}
}
