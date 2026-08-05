package omnaralint

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis/analysistest"
)

func TestAnalyzerRejectsBitShifts(t *testing.T) {
	analysistest.Run(t, analysistest.TestData(), Analyzer, "bitshift")
}

func TestAnalyzerRejectsStorageWallClockAuthority(t *testing.T) {
	analysistest.Run(
		t,
		analysistest.TestData(),
		Analyzer,
		"github.com/omnara-ai/omnara/internal/storage/timestampownership",
	)
}

func TestAnalyzerRejectsDirectTimeOnExportedStorageMutationMethods(t *testing.T) {
	analysistest.Run(
		t,
		analysistest.TestData(),
		Analyzer,
		"github.com/omnara-ai/omnara/internal/storage",
	)
}

func TestAnalyzerRejectsContextUnawareProductionSleep(t *testing.T) {
	analysistest.Run(
		t,
		analysistest.TestData(),
		Analyzer,
		"github.com/omnara-ai/omnara/internal/polling",
	)
}

func TestAnalyzerExcludesGeneratedStorageCodeFromTimeRules(t *testing.T) {
	analysistest.Run(
		t,
		analysistest.TestData(),
		Analyzer,
		"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc",
	)
}

func TestAnalyzerUsesPackageIdentityForStorageRules(t *testing.T) {
	analysistest.Run(
		t,
		analysistest.TestData(),
		Analyzer,
		"example.com/internal/storage/lookalike",
	)
}

func TestStateSQLWildcardScan(t *testing.T) {
	queries := filepath.Join(t.TempDir(), "queries")
	if err := os.MkdirAll(queries, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(queries, "query.sql"),
		[]byte("SELECT * FROM records;\nINSERT INTO records(value) VALUES (?) RETURNING records.*;\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	issues := scanStateSQLWildcardIssues(queries)
	if len(issues) != 2 {
		t.Fatalf("wildcard issues = %v, want 2", issues)
	}
}
