//go:build integration

package agentexecution_test

import (
	"net/url"
	"os"
	"os/exec"
	"testing"

	"github.com/omnara-ai/omnara/internal/testutil/integrationdb"
	"github.com/stretchr/testify/require"
)

func TestExecutionSQLCPrepare(t *testing.T) {
	pool := integrationdb.OpenMigratedPool(t, t.Context(), "../../../../migrations")
	command := exec.CommandContext(t.Context(), "go", "tool", "-modfile=tools/ci/go.mod",
		"github.com/sqlc-dev/sqlc/cmd/sqlc", "vet", "-f", "sqlc.vet-db.yaml")
	command.Dir = "../../../.."
	databaseURL, err := url.Parse(os.Getenv("OMNARA_TEST_DATABASE_URL"))
	require.NoError(t, err)
	databaseURL.Path = "/" + pool.Config().ConnConfig.Database
	command.Env = append(os.Environ(), "SQLC_DATABASE_URL="+databaseURL.String())
	output, err := command.CombinedOutput()
	require.NoError(t, err, string(output))
}
