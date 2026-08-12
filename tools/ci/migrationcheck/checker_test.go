package main

import (
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCheckSnapshotAcceptsCanonicalMigrations(t *testing.T) {
	source := validMemorySnapshot()
	if err := checkSnapshot(source); err != nil {
		t.Fatalf("check canonical snapshot: %v", err)
	}
}

func TestCheckSnapshotRejectsInvalidMigrationTrees(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(memorySnapshot)
		want   string
	}{
		{
			name: "missing server migrations",
			mutate: func(source memorySnapshot) {
				delete(source, "migrations/000001_initial.sql")
			},
			want: "migrations contains no SQL migrations",
		},
		{
			name: "missing daemon migrations",
			mutate: func(source memorySnapshot) {
				delete(source, "internal/machinedaemon/statedb/migrations/000001_initial.sql")
			},
			want: "internal/machinedaemon/statedb/migrations contains no SQL migrations",
		},
		{
			name: "noncanonical server filename",
			mutate: func(source memorySnapshot) {
				delete(source, "migrations/000001_initial.sql")
				source["migrations/1_initial.sql"] = []byte("server")
			},
			want: "does not use NNNNNN_name.sql",
		},
		{
			name: "noncanonical daemon filename",
			mutate: func(source memorySnapshot) {
				delete(source, "internal/machinedaemon/statedb/migrations/000001_initial.sql")
				source["internal/machinedaemon/statedb/migrations/000001.sql"] = []byte("daemon")
			},
			want: "does not use NNNNNN_name.sql",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := validMemorySnapshot()
			test.mutate(source)
			if err := checkSnapshot(source); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("check error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCompareSnapshotsFreezesReleasedMigrations(t *testing.T) {
	base := validMemorySnapshot()
	current := validMemorySnapshot()
	if err := compareSnapshots(base, current); err != nil {
		t.Fatalf("compare unchanged release: %v", err)
	}

	current["migrations/000001_initial.sql"] = []byte("changed")
	if err := compareSnapshots(base, current); err == nil || !strings.Contains(err.Error(), "was modified") {
		t.Fatalf("changed historical migration error = %v", err)
	}
	current = validMemorySnapshot()
	delete(current, "migrations/000001_initial.sql")
	err := compareSnapshots(base, current)
	if err == nil || !strings.Contains(err.Error(), "deleted or renamed") {
		t.Fatalf("deleted historical migration error = %v", err)
	}
}

func TestCompareSnapshotsAllowsNewMigrationSuffix(t *testing.T) {
	base := validMemorySnapshot()
	current := validMemorySnapshot()
	current["migrations/000002_next.sql"] = []byte("next")
	if err := checkSnapshot(current); err != nil {
		t.Fatal(err)
	}
	if err := compareSnapshots(base, current); err != nil {
		t.Fatalf("compare appended migration: %v", err)
	}
}

func TestCompareReleasedRepositoryUsesIndependentReleaseStreams(t *testing.T) {
	root := newMigrationGitRepository(t)
	writeMigrationTestFile(t, root, "migrations/000001_initial.sql", "server released")
	writeMigrationTestFile(
		t,
		root,
		"internal/machinedaemon/statedb/migrations/000001_initial.sql",
		"daemon released",
	)
	runMigrationGit(t, root, "add", ".")
	runMigrationGit(t, root, "commit", "-m", "first release")
	runMigrationGit(t, root, "tag", "cluster-v1.0.0")
	runMigrationGit(t, root, "tag", "omnarad-v1.0.0")

	writeMigrationTestFile(t, root, "migrations/000002_draft.sql", "server draft one")
	writeMigrationTestFile(
		t,
		root,
		"internal/machinedaemon/statedb/migrations/000002_next.sql",
		"daemon released two",
	)
	runMigrationGit(t, root, "add", ".")
	runMigrationGit(t, root, "commit", "-m", "daemon release")
	runMigrationGit(t, root, "tag", "omnarad-v1.1.0")

	writeMigrationTestFile(t, root, "migrations/000002_draft.sql", "server draft two")
	if err := compareReleasedRepository(root, "refs/tags"); err != nil {
		t.Fatalf("edit unreleased server migration: %v", err)
	}

	writeMigrationTestFile(
		t,
		root,
		"internal/machinedaemon/statedb/migrations/000002_next.sql",
		"changed daemon release",
	)
	err := compareReleasedRepository(root, "refs/tags")
	if err == nil || !strings.Contains(err.Error(), "release omnarad-v1.1.0") ||
		!strings.Contains(err.Error(), "released migration") {
		t.Fatalf("changed released daemon migration error = %v", err)
	}
}

func TestCompareReleasedRepositoryUsesOnlyConfiguredReleaseRefs(t *testing.T) {
	root := newMigrationGitRepository(t)
	writeMigrationTestFile(t, root, "migrations/000001_initial.sql", "server released")
	writeMigrationTestFile(
		t,
		root,
		"internal/machinedaemon/statedb/migrations/000001_initial.sql",
		"daemon released",
	)
	runMigrationGit(t, root, "add", ".")
	runMigrationGit(t, root, "commit", "-m", "trusted release")
	releaseCommit := runMigrationGit(t, root, "rev-parse", "HEAD")
	const releaseRefRoot = "refs/omnara/test-releases"
	runMigrationGit(t, root, "update-ref", releaseRefRoot+"/cluster-v1.0.0", releaseCommit)
	runMigrationGit(t, root, "update-ref", releaseRefRoot+"/omnarad-v1.0.0", releaseCommit)

	writeMigrationTestFile(t, root, "migrations/000002_draft.sql", "server draft one")
	writeMigrationTestFile(
		t,
		root,
		"internal/machinedaemon/statedb/migrations/000002_draft.sql",
		"daemon draft one",
	)
	runMigrationGit(t, root, "add", ".")
	runMigrationGit(t, root, "commit", "-m", "local-only tags")
	runMigrationGit(t, root, "tag", "cluster-v999.0.0")
	runMigrationGit(t, root, "tag", "omnarad-v999.0.0")

	writeMigrationTestFile(t, root, "migrations/000002_draft.sql", "server draft two")
	writeMigrationTestFile(
		t,
		root,
		"internal/machinedaemon/statedb/migrations/000002_draft.sql",
		"daemon draft two",
	)
	if err := compareReleasedRepository(root, releaseRefRoot); err != nil {
		t.Fatalf("configured release refs should ignore ambient tags: %v", err)
	}
	if err := compareReleasedRepository(root, "refs/tags"); err == nil ||
		!strings.Contains(err.Error(), "released migration") {
		t.Fatalf("ambient tag comparison error = %v, want released migration", err)
	}
}

func TestLatestReleaseBoundaryUsesSemanticVersionOrder(t *testing.T) {
	root := newMigrationGitRepository(t)
	writeMigrationTestFile(t, root, "README", "first")
	runMigrationGit(t, root, "add", ".")
	runMigrationGit(t, root, "commit", "-m", "first")
	runMigrationGit(t, root, "tag", "cluster-v1.9.0")
	writeMigrationTestFile(t, root, "README", "second")
	runMigrationGit(t, root, "add", ".")
	runMigrationGit(t, root, "commit", "-m", "second")
	wantCommit := runMigrationGit(t, root, "rev-parse", "HEAD")
	runMigrationGit(t, root, "tag", "cluster-v1.10.0")

	boundary, err := latestReleaseBoundary(root, "refs/tags", "cluster-v")
	if err != nil {
		t.Fatal(err)
	}
	if boundary.tag != "cluster-v1.10.0" || boundary.commit != wantCommit {
		t.Fatalf("latest boundary = %#v, want tag cluster-v1.10.0 at %s", boundary, wantCommit)
	}
}

func TestLatestReleaseBoundaryFailsClosed(t *testing.T) {
	root := newMigrationGitRepository(t)
	writeMigrationTestFile(t, root, "README", "first")
	runMigrationGit(t, root, "add", ".")
	runMigrationGit(t, root, "commit", "-m", "first")

	if _, err := latestReleaseBoundary(root, "refs/tags", "cluster-v"); err == nil ||
		!strings.Contains(err.Error(), "fetch trusted release tags") {
		t.Fatalf("missing release tags error = %v", err)
	}
	runMigrationGit(t, root, "tag", "cluster-v01.2.3")
	if _, err := latestReleaseBoundary(root, "refs/tags", "cluster-v"); err == nil ||
		!strings.Contains(err.Error(), "MAJOR.MINOR.PATCH") {
		t.Fatalf("malformed release tag error = %v", err)
	}
}

func TestParseReleaseTag(t *testing.T) {
	got, err := parseReleaseTag("cluster-v12.34.56", "cluster-v")
	if err != nil {
		t.Fatal(err)
	}
	if got != [3]uint64{12, 34, 56} {
		t.Fatalf("parsed version = %v", got)
	}
	for _, invalid := range []string{
		"cluster-v1.2",
		"cluster-v1.2.3.4",
		"cluster-v01.2.3",
		"cluster-v1.02.3",
		"cluster-v1.2.03",
		"cluster-v1.2.x",
		"cluster-v1.2.3-rc1",
		"other-v1.2.3",
		"cluster-v18446744073709551616.0.0",
	} {
		if _, err := parseReleaseTag(invalid, "cluster-v"); err == nil {
			t.Fatalf("invalid release tag %q accepted", invalid)
		}
	}
}

func TestFullObjectIDValidation(t *testing.T) {
	if !isFullObjectID(strings.Repeat("a", 40)) || !isFullObjectID(strings.Repeat("F", 64)) {
		t.Fatal("valid full object ID rejected")
	}
	for _, invalid := range []string{"", "HEAD", strings.Repeat("a", 39), strings.Repeat("g", 40)} {
		if isFullObjectID(invalid) {
			t.Fatalf("invalid full object ID %q accepted", invalid)
		}
	}
}

type memorySnapshot map[string][]byte

func validMemorySnapshot() memorySnapshot {
	return memorySnapshot{
		"migrations/000001_initial.sql":                                []byte("server"),
		"internal/machinedaemon/statedb/migrations/000001_initial.sql": []byte("daemon"),
	}
}

func (source memorySnapshot) listSQL(directory string) ([]string, error) {
	var files []string
	for filePath := range source {
		if path.Dir(filePath) == directory && path.Ext(filePath) == ".sql" {
			files = append(files, filePath)
		}
	}
	sort.Strings(files)
	return files, nil
}

func (source memorySnapshot) readFile(filePath string) ([]byte, error) {
	body, ok := source[filePath]
	if !ok {
		return nil, fs.ErrNotExist
	}
	return body, nil
}

func (source memorySnapshot) readOptional(filePath string) ([]byte, bool, error) {
	body, ok := source[filePath]
	return body, ok, nil
}

func compareSnapshots(base snapshot, current currentSnapshot) error {
	for _, set := range migrationSets {
		if err := compareMigrationSet(set, base, current); err != nil {
			return err
		}
	}
	return nil
}

func writeMigrationTestFile(t *testing.T, root, filePath, body string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(filePath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newMigrationGitRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	runMigrationGit(t, root, "init", "--initial-branch=main")
	runMigrationGit(t, root, "config", "user.name", "Migration Test")
	runMigrationGit(t, root, "config", "user.email", "migration@example.com")
	return root
}

func runMigrationGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
