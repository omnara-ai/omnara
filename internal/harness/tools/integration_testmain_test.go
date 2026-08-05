//go:build integration

package tools

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestMain(m *testing.M) {
	integrationdb.RunTestMain(m)
}
