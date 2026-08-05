//go:build integration

package httpapi

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestMain(m *testing.M) {
	integrationdb.RunTestMain(m)
}
