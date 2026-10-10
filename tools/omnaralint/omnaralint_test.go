package omnaralint

import (
	"go/parser"
	"go/token"
	"go/types"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/tools/go/analysis"

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

func TestAnalyzerRejectsUnjustifiedTestSleep(t *testing.T) {
	analysistest.Run(
		t,
		analysistest.TestData(),
		Analyzer,
		"github.com/omnara-ai/omnara/internal/testsleep",
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

func TestExecutionSQLBoundary(t *testing.T) {
	for _, sql := range []string{
		`INSERT INTO agents(id) VALUES ($1)`,
		`INSERT /* nested /* comment */ */ INTO "public"."agent_inputs"(id) VALUES ($1)`,
		`UPDATE tool_calls SET state='ready' WHERE id=$1`,
		`WITH changed AS (DELETE FROM agent_events WHERE id=$1 RETURNING id) SELECT id FROM changed`,
		`COPY agents FROM STDIN`,
		`MERGE INTO agent_inputs a USING old_inputs b ON a.id=b.id WHEN MATCHED THEN DELETE`,
		`TRUNCATE agents`,
		`DO $$ BEGIN DELETE FROM agents; END $$`,
	} {
		t.Run(sql, func(t *testing.T) {
			writes, err := executionSQL(sql)
			if err != nil || !writes {
				t.Fatalf("writes=%v error=%v", writes, err)
			}
		})
	}
	for _, sql := range []string{`SELECT id FROM agents WHERE id=$1 FOR UPDATE`,
		`SELECT 'INSERT INTO agents'`,
		`UPDATE machines SET display_name=$2 WHERE id=$1`} {
		writes, err := executionSQL(sql)
		if err != nil || writes {
			t.Fatalf("%s: writes=%v error=%v", sql, writes, err)
		}
	}
}

func TestExecutionGoBoundary(t *testing.T) {
	analysistest.Run(
		t,
		analysistest.TestData(),
		Analyzer,
		"github.com/omnara-ai/omnara/internal/storage/executionstore",
	)
}

func TestExecutionPrivateImport(t *testing.T) {
	for _, test := range []struct{ path, message string }{
		{executionPackage + "/internal/executiondb", "executiondb imports belong to agentexecution"},
		{testutilPackage + "/storagetest", "production code cannot import execution test fixtures"},
	} {
		t.Run(test.path, func(t *testing.T) {
			set := token.NewFileSet()
			file, err := parser.ParseFile(set, "escape.go", "package executionstore\n import \""+test.path+"\"", 0)
			if err != nil {
				t.Fatal(err)
			}
			var messages []string
			pass := &analysis.Pass{
				Fset:   set,
				Pkg:    types.NewPackage(storagePackage+"/executionstore", "executionstore"),
				Report: func(d analysis.Diagnostic) { messages = append(messages, d.Message) },
			}
			checkExecutionAccess(pass, file, false)
			if len(messages) != 1 || messages[0] != test.message {
				t.Fatalf("diagnostics=%v", messages)
			}
		})
	}
}
