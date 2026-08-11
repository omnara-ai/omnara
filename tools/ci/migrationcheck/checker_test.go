package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func TestCheckSnapshotAcceptsCanonicalManifests(t *testing.T) {
	source := validMemorySnapshot()
	if _, err := checkSnapshot(source); err != nil {
		t.Fatalf("check canonical snapshot: %v", err)
	}
}

func TestCheckSnapshotRejectsManifestDrift(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(memorySnapshot)
		want   string
	}{
		{
			name: "changed migration",
			mutate: func(source memorySnapshot) {
				source["migrations/000001_initial.sql"] = []byte("changed")
			},
			want: "does not match its committed checksum",
		},
		{
			name: "missing entry",
			mutate: func(source memorySnapshot) {
				source["migrations/checksums.sha256"] = nil
			},
			want: "manifest must be non-empty",
		},
		{
			name: "uppercase hash",
			mutate: func(source memorySnapshot) {
				source["migrations/checksums.sha256"] = bytes.ToUpper(source["migrations/checksums.sha256"])
			},
			want: "checksum must be lowercase",
		},
		{
			name: "noncanonical separator",
			mutate: func(source memorySnapshot) {
				source["migrations/checksums.sha256"] = bytes.Replace(
					source["migrations/checksums.sha256"], []byte("  "), []byte(" "), 1,
				)
			},
			want: "must be '<lowercase sha256>  <path>'",
		},
		{
			name: "blank line",
			mutate: func(source memorySnapshot) {
				source["migrations/checksums.sha256"] = append(source["migrations/checksums.sha256"], '\n')
			},
			want: "must not contain blank lines",
		},
		{
			name: "extra SQL file",
			mutate: func(source memorySnapshot) {
				source["migrations/000002_next.sql"] = []byte("next")
			},
			want: "describes 1 migrations",
		},
		{
			name: "deleted SQL file",
			mutate: func(source memorySnapshot) {
				delete(source, "migrations/000001_initial.sql")
			},
			want: "contains no SQL migrations",
		},
		{
			name: "daemon drift",
			mutate: func(source memorySnapshot) {
				source["internal/machinedaemon/statedb/migrations/000001_initial.sql"] = []byte("changed")
			},
			want: "does not match its committed checksum",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := validMemorySnapshot()
			test.mutate(source)
			if _, err := checkSnapshot(source); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("check error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestParseManifestRejectsDuplicateUnsortedAndOutsidePaths(t *testing.T) {
	set := migrationSet{directory: "migrations", manifest: "migrations/checksums.sha256"}
	digest := sha256.Sum256([]byte("migration"))
	tests := []struct {
		name string
		body string
		want string
	}{
		{
			name: "duplicate",
			body: checksumLine(digest, "migrations/000001_first.sql") +
				checksumLine(digest, "migrations/000001_first.sql"),
			want: "unique and sorted",
		},
		{
			name: "unsorted",
			body: checksumLine(digest, "migrations/000002_second.sql") +
				checksumLine(digest, "migrations/000001_first.sql"),
			want: "unique and sorted",
		},
		{
			name: "outside",
			body: checksumLine(digest, "other/000001_first.sql"),
			want: "outside migrations",
		},
		{
			name: "noncanonical name",
			body: checksumLine(digest, "migrations/1_first.sql"),
			want: "does not use NNNNNN_name.sql",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := parseManifest(set, []byte(test.body)); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("parse error = %v, want substring %q", err, test.want)
			}
		})
	}
}

func TestCompareSnapshotsFreezesBaseWithoutManifest(t *testing.T) {
	base := validMemorySnapshot()
	delete(base, "migrations/checksums.sha256")
	delete(base, "internal/machinedaemon/statedb/migrations/checksums.sha256")

	current := validMemorySnapshot()
	manifests, err := checkSnapshot(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := compareSnapshots(base, current, manifests); err != nil {
		t.Fatalf("compare unchanged bootstrap base: %v", err)
	}

	current["migrations/000001_initial.sql"] = []byte("changed")
	if err := compareSnapshots(base, current, manifests); err == nil || !strings.Contains(err.Error(), "was modified") {
		t.Fatalf("changed historical migration error = %v", err)
	}
	delete(current, "migrations/000001_initial.sql")
	err = compareSnapshots(base, current, manifests)
	if err == nil || !strings.Contains(err.Error(), "deleted or renamed") {
		t.Fatalf("deleted historical migration error = %v", err)
	}
}

func TestCompareSnapshotsAllowsNewMigrationSuffix(t *testing.T) {
	base := validMemorySnapshot()
	current := validMemorySnapshot()
	current["migrations/000002_next.sql"] = []byte("next")
	current.refreshManifests()
	manifests, err := checkSnapshot(current)
	if err != nil {
		t.Fatal(err)
	}
	if err := compareSnapshots(base, current, manifests); err != nil {
		t.Fatalf("compare appended migration: %v", err)
	}
}

func TestCompareSnapshotsFreezesTrustedManifestEntries(t *testing.T) {
	base := validMemorySnapshot()
	current := validMemorySnapshot()
	manifests, err := checkSnapshot(current)
	if err != nil {
		t.Fatal(err)
	}

	wrongDigest := sha256.Sum256([]byte("different trusted bytes"))
	base["migrations/checksums.sha256"] = []byte(
		checksumLine(wrongDigest, "migrations/000001_initial.sql"),
	)
	err = compareSnapshots(base, current, manifests)
	if err == nil || !strings.Contains(err.Error(), "released checksum entry") {
		t.Fatalf("changed trusted manifest error = %v", err)
	}
}

func TestUpdateRepositoryBootstrapsAppendsAndRefreshesCurrentSnapshot(t *testing.T) {
	root := t.TempDir()
	writeMigrationTestFile(t, root, "migrations/000001_initial.sql", "first")
	writeMigrationTestFile(t, root, "internal/machinedaemon/statedb/migrations/000001_initial.sql", "daemon")

	if err := updateRepository(root); err != nil {
		t.Fatalf("bootstrap manifests: %v", err)
	}
	if err := checkRepository(root); err != nil {
		t.Fatalf("check bootstrapped manifests: %v", err)
	}
	writeMigrationTestFile(t, root, "migrations/000002_next.sql", "second")
	if err := updateRepository(root); err != nil {
		t.Fatalf("append manifest entry: %v", err)
	}
	if err := checkRepository(root); err != nil {
		t.Fatalf("check appended manifest: %v", err)
	}

	writeMigrationTestFile(t, root, "migrations/000001_initial.sql", "rewritten")
	if err := updateRepository(root); err != nil {
		t.Fatalf("refresh changed snapshot: %v", err)
	}
	if err := checkRepository(root); err != nil {
		t.Fatalf("check refreshed snapshot: %v", err)
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
	refreshMigrationTestManifests(t, root)
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
	refreshMigrationTestManifests(t, root)
	runMigrationGit(t, root, "add", ".")
	runMigrationGit(t, root, "commit", "-m", "daemon release")
	runMigrationGit(t, root, "tag", "omnarad-v1.1.0")

	writeMigrationTestFile(t, root, "migrations/000002_draft.sql", "server draft two")
	refreshMigrationTestManifests(t, root)
	if err := compareReleasedRepository(root); err != nil {
		t.Fatalf("edit unreleased server migration: %v", err)
	}

	writeMigrationTestFile(
		t,
		root,
		"internal/machinedaemon/statedb/migrations/000002_next.sql",
		"changed daemon release",
	)
	refreshMigrationTestManifests(t, root)
	err := compareReleasedRepository(root)
	if err == nil || !strings.Contains(err.Error(), "release omnarad-v1.1.0") ||
		!strings.Contains(err.Error(), "released migration") {
		t.Fatalf("changed released daemon migration error = %v", err)
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

	boundary, err := latestReleaseBoundary(root, "cluster-v")
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

	if _, err := latestReleaseBoundary(root, "cluster-v"); err == nil ||
		!strings.Contains(err.Error(), "fetch trusted release tags") {
		t.Fatalf("missing release tags error = %v", err)
	}
	runMigrationGit(t, root, "tag", "cluster-v01.2.3")
	if _, err := latestReleaseBoundary(root, "cluster-v"); err == nil ||
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
	source := memorySnapshot{
		"migrations/000001_initial.sql":                                []byte("server"),
		"internal/machinedaemon/statedb/migrations/000001_initial.sql": []byte("daemon"),
	}
	source.refreshManifests()
	return source
}

func (source memorySnapshot) refreshManifests() {
	for _, set := range migrationSets {
		files := make(map[string][]byte)
		for filePath, body := range source {
			if path.Dir(filePath) == set.directory && path.Ext(filePath) == ".sql" {
				files[filePath] = body
			}
		}
		source[set.manifest] = renderManifest(files)
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

func checksumLine(digest [sha256.Size]byte, filePath string) string {
	return hexDigest(digest) + "  " + filePath + "\n"
}

func hexDigest(digest [sha256.Size]byte) string {
	return hex.EncodeToString(digest[:])
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

func refreshMigrationTestManifests(t *testing.T, root string) {
	t.Helper()
	if err := updateRepository(root); err != nil {
		t.Fatalf("refresh migration manifests: %v", err)
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
