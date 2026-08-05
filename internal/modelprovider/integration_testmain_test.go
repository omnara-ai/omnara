//go:build integration

package modelprovider

import (
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
)

func TestMain(m *testing.M) {
	integrationdb.RunTestMain(m)
}
