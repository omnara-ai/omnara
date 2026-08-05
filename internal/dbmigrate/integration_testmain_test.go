//go:build integration

package dbmigrate_test

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestMain(m *testing.M) {
	integrationdb.RunTestMain(m)
}
