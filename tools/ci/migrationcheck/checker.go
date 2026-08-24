package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

type migrationSet struct {
	directory        string
	releaseTagPrefix string
	allowGo          bool
}

// The registry evolves; numbered migration implementations remain release-frozen.
const goMigrationRegistryPath = "migrations/go_migrations.go"

var migrationSets = []migrationSet{
	{
		directory:        "internal/machinedaemon/statedb/migrations",
		releaseTagPrefix: "omnarad-v",
	},
	{
		directory:        "migrations",
		releaseTagPrefix: "cluster-v",
		allowGo:          true,
	},
}

type snapshot interface {
	listMigrations(directory string) ([]string, error)
	readFile(filePath string) ([]byte, error)
}

type currentSnapshot interface {
	snapshot
	readOptional(filePath string) ([]byte, bool, error)
}

func checkRepository(root string) error {
	return checkSnapshot(worktreeSnapshot{root: root})
}

// Publication starts on tag push; see .github/workflows/cluster-release.yaml
// and .github/workflows/omnarad-release.yaml.
func compareReleasedRepository(root, releaseRefRoot string) error {
	current := worktreeSnapshot{root: root}
	if err := checkSnapshot(current); err != nil {
		return err
	}
	for _, set := range migrationSets {
		boundary, err := latestReleaseBoundary(root, releaseRefRoot, set.releaseTagPrefix)
		if err != nil {
			return fmt.Errorf("resolve release boundary for %s: %w", set.directory, err)
		}
		base := gitSnapshot{root: root, ref: boundary.commit}
		if err := base.verifyCommit(); err != nil {
			return fmt.Errorf("verify release boundary %s: %w", boundary.tag, err)
		}
		if err := compareMigrationSet(set, base, current); err != nil {
			return fmt.Errorf("release %s: %w", boundary.tag, err)
		}
	}
	return nil
}

func checkSnapshot(source snapshot) error {
	for _, set := range migrationSets {
		files, err := source.listMigrations(set.directory)
		if err != nil {
			return fmt.Errorf("list %s: %w", set.directory, err)
		}
		if len(files) == 0 {
			return fmt.Errorf("%s contains no migrations", set.directory)
		}
		previousFile := ""
		previousVersion := 0
		for index, filePath := range files {
			if err := validateMigrationPath(set, filePath); err != nil {
				return err
			}
			version, err := strconv.Atoi(path.Base(filePath)[:6])
			if err != nil {
				return fmt.Errorf("parse migration version for %s: %w", filePath, err)
			}
			if index > 0 && version == previousVersion {
				return fmt.Errorf(
					"%s duplicates migration version %06d from %s",
					filePath,
					version,
					previousFile,
				)
			}
			if expected := index + 1; version != expected {
				return fmt.Errorf(
					"%s must be migration %06d; run make migration-fix after rebasing",
					filePath,
					expected,
				)
			}
			previousFile = filePath
			previousVersion = version
		}
	}
	return nil
}

func compareMigrationSet(set migrationSet, base snapshot, current currentSnapshot) error {
	baseFiles, err := base.listMigrations(set.directory)
	if err != nil {
		return fmt.Errorf("list released %s: %w", set.directory, err)
	}
	for _, filePath := range baseFiles {
		before, err := base.readFile(filePath)
		if err != nil {
			return fmt.Errorf("read released migration %s: %w", filePath, err)
		}
		after, exists, err := current.readOptional(filePath)
		if err != nil {
			return fmt.Errorf("read current migration %s: %w", filePath, err)
		}
		if !exists {
			return fmt.Errorf("released migration %s was deleted or renamed", filePath)
		}
		if !bytes.Equal(before, after) {
			return fmt.Errorf("released migration %s was modified", filePath)
		}
	}
	return nil
}

type releaseBoundary struct {
	tag     string
	ref     string
	commit  string
	version [3]uint64
}

func latestReleaseBoundary(root, releaseRefRoot, prefix string) (releaseBoundary, error) {
	releaseRefPrefix := strings.TrimSuffix(releaseRefRoot, "/") + "/"
	command := exec.CommandContext(
		context.Background(),
		"git",
		"-C",
		root,
		"for-each-ref",
		"--format=%(refname)",
		releaseRefPrefix+prefix+"*",
	)
	output, err := command.Output()
	if err != nil {
		return releaseBoundary{}, fmt.Errorf("list %s release tags: %w", prefix, err)
	}
	var latest releaseBoundary
	for _, ref := range strings.Fields(string(output)) {
		tag, found := strings.CutPrefix(ref, releaseRefPrefix)
		if !found || strings.Contains(tag, "/") {
			return releaseBoundary{}, fmt.Errorf("release ref %q is outside %s", ref, releaseRefPrefix)
		}
		version, err := parseReleaseTag(tag, prefix)
		if err != nil {
			return releaseBoundary{}, err
		}
		if latest.tag == "" || compareReleaseVersions(version, latest.version) > 0 {
			latest = releaseBoundary{tag: tag, ref: ref, version: version}
		}
	}
	if latest.tag == "" {
		return releaseBoundary{}, fmt.Errorf(
			"no %sMAJOR.MINOR.PATCH tag is available; fetch trusted release tags before checking migrations",
			prefix,
		)
	}
	commitCommand := exec.CommandContext(
		context.Background(),
		"git",
		"-C",
		root,
		"rev-parse",
		"--verify",
		latest.ref+"^{commit}",
	)
	commitOutput, err := commitCommand.Output()
	if err != nil {
		return releaseBoundary{}, fmt.Errorf("resolve release tag %s: %w", latest.tag, err)
	}
	latest.commit = strings.TrimSpace(string(commitOutput))
	if !isFullObjectID(latest.commit) {
		return releaseBoundary{}, fmt.Errorf(
			"release tag %s resolved to invalid commit %q",
			latest.tag,
			latest.commit,
		)
	}
	return latest, nil
}

func parseReleaseTag(tag, prefix string) ([3]uint64, error) {
	versionText, found := strings.CutPrefix(tag, prefix)
	if !found {
		return [3]uint64{}, fmt.Errorf("release tag %q must start with %s", tag, prefix)
	}
	parts := strings.Split(versionText, ".")
	if len(parts) != 3 {
		return [3]uint64{}, fmt.Errorf("release tag %q must use %sMAJOR.MINOR.PATCH", tag, prefix)
	}
	var version [3]uint64
	for index, part := range parts {
		if part == "" || len(part) > 1 && part[0] == '0' {
			return [3]uint64{}, fmt.Errorf("release tag %q must use %sMAJOR.MINOR.PATCH", tag, prefix)
		}
		for _, character := range part {
			if character < '0' || character > '9' {
				return [3]uint64{}, fmt.Errorf(
					"release tag %q must use %sMAJOR.MINOR.PATCH",
					tag,
					prefix,
				)
			}
		}
		value, err := strconv.ParseUint(part, 10, 64)
		if err != nil {
			return [3]uint64{}, fmt.Errorf("release tag %q must use %sMAJOR.MINOR.PATCH", tag, prefix)
		}
		version[index] = value
	}
	return version, nil
}

func compareReleaseVersions(left, right [3]uint64) int {
	for index := range left {
		if left[index] < right[index] {
			return -1
		}
		if left[index] > right[index] {
			return 1
		}
	}
	return 0
}

func validateMigrationPath(set migrationSet, filePath string) error {
	if filePath == "" || filepath.ToSlash(filePath) != filePath || path.Clean(filePath) != filePath {
		return fmt.Errorf("migration path %q is not canonical", filePath)
	}
	extension := path.Ext(filePath)
	if path.Dir(filePath) != set.directory || extension != ".sql" && !(set.allowGo && extension == ".go") {
		return fmt.Errorf("migration path %q is outside %s", filePath, set.directory)
	}
	base := path.Base(filePath)
	if len(base) < 8+len(extension) || base[6] != '_' || !allDecimal(base[:6]) || base[7:len(base)-len(extension)] == "" {
		return fmt.Errorf("migration path %q does not use NNNNNN_name%s", filePath, extension)
	}
	return nil
}

func allDecimal(value string) bool {
	for i := range len(value) {
		if value[i] < '0' || value[i] > '9' {
			return false
		}
	}
	return true
}

type worktreeSnapshot struct{ root string }

func (snapshot worktreeSnapshot) listMigrations(directory string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(snapshot.root, filepath.FromSlash(directory)))
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		filePath := path.Join(directory, entry.Name())
		if !isMigrationFile(filePath) {
			continue
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("%s/%s must be a regular file", directory, entry.Name())
		}
		files = append(files, filePath)
	}
	sort.Strings(files)
	return files, nil
}

func (snapshot worktreeSnapshot) readFile(filePath string) ([]byte, error) {
	fullPath := filepath.Join(snapshot.root, filepath.FromSlash(filePath))
	info, err := os.Lstat(fullPath)
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("%s must be a regular file", filePath)
	}
	return os.ReadFile(fullPath)
}

func (snapshot worktreeSnapshot) readOptional(filePath string) ([]byte, bool, error) {
	body, err := snapshot.readFile(filePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, false, nil
	}
	return body, err == nil, err
}

type gitSnapshot struct {
	root string
	ref  string
}

func (snapshot gitSnapshot) verifyCommit() error {
	command := snapshot.command("cat-file", "-e", snapshot.ref+"^{commit}")
	if output, err := command.CombinedOutput(); err != nil {
		return fmt.Errorf("trusted Git base %s is unavailable: %s: %w", snapshot.ref, strings.TrimSpace(string(output)), err)
	}
	return nil
}

func (snapshot gitSnapshot) listMigrations(directory string) ([]string, error) {
	command := snapshot.command("ls-tree", "-r", "-z", "--name-only", snapshot.ref, "--", directory)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, candidate := range bytes.Split(output, []byte{0}) {
		filePath := string(candidate)
		if filePath != "" && path.Dir(filePath) == directory && isMigrationFile(filePath) {
			files = append(files, filePath)
		}
	}
	sort.Strings(files)
	return files, nil
}

func isMigrationFile(filePath string) bool {
	if filePath == goMigrationRegistryPath {
		return false
	}
	extension := path.Ext(filePath)
	return extension == ".sql" || extension == ".go" && !strings.HasSuffix(filePath, "_test.go")
}

func (snapshot gitSnapshot) readFile(filePath string) ([]byte, error) {
	command := snapshot.command("cat-file", "blob", snapshot.ref+":"+filePath)
	return command.Output()
}

func (snapshot gitSnapshot) command(arguments ...string) *exec.Cmd {
	gitArguments := append([]string{"-C", snapshot.root}, arguments...)
	return exec.CommandContext(context.Background(), "git", gitArguments...)
}

func isFullObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for i := range len(value) {
		if !((value[i] >= '0' && value[i] <= '9') || (value[i] >= 'a' && value[i] <= 'f') ||
			(value[i] >= 'A' && value[i] <= 'F')) {
			return false
		}
	}
	return true
}
