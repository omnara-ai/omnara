package migrations

import "github.com/pressly/goose/v3"

// GoMigrations returns fresh registrations for every versioned Go migration in this directory.
func GoMigrations() []*goose.Migration {
	return []*goose.Migration{
		newAgentConfigNameMigration(),
	}
}
