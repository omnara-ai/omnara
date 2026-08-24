package migrations

import "github.com/pressly/goose/v3"

func GoMigrations() []*goose.Migration {
	return []*goose.Migration{
		newAgentConfigNameMigration(),
	}
}
