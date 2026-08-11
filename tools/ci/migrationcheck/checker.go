package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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
	manifest         string
	releaseTagPrefix string
}

var migrationSets = []migrationSet{
	{
		directory:        "internal/machinedaemon/statedb/migrations",
		manifest:         "internal/machinedaemon/statedb/migrations/checksums.sha256",
		releaseTagPrefix: "omnarad-v",
	},
	{
		directory:        "migrations",
		manifest:         "migrations/checksums.sha256",
		releaseTagPrefix: "cluster-v",
	},
}

type snapshot interface {
	listSQL(directory string) ([]string, error)
	readFile(filePath string) ([]byte, error)
	readOptional(filePath string) ([]byte, bool, error)
}

type manifest map[string][sha256.Size]byte

func checkRepository(root string) error {
	_, err := checkSnapshot(worktreeSnapshot{root: root})
	return err
}

// A release tag is the immutability boundary because publishing starts when the
// tag is pushed. Waiting for the final GitHub Release would miss images exposed
// by a partially completed publish workflow.
func compareReleasedRepository(root string) error {
	current := worktreeSnapshot{root: root}
	currentManifests, err := checkSnapshot(current)
	if err != nil {
		return err
	}
	for _, set := range migrationSets {
		boundary, err := latestReleaseBoundary(root, set.releaseTagPrefix)
		if err != nil {
			return fmt.Errorf("resolve release boundary for %s: %w", set.directory, err)
		}
		base := gitSnapshot{root: root, ref: boundary.commit}
		if err := base.verifyCommit(); err != nil {
			return fmt.Errorf("verify release boundary %s: %w", boundary.tag, err)
		}
		if err := compareMigrationSet(
			set,
			base,
			current,
			currentManifests[set.manifest],
		); err != nil {
			return fmt.Errorf("release %s: %w", boundary.tag, err)
		}
	}
	return nil
}

func checkSnapshot(source snapshot) (map[string]manifest, error) {
	manifests := make(map[string]manifest, len(migrationSets))
	for _, set := range migrationSets {
		files, err := source.listSQL(set.directory)
		if err != nil {
			return nil, fmt.Errorf("list %s: %w", set.directory, err)
		}
		if len(files) == 0 {
			return nil, fmt.Errorf("%s contains no SQL migrations", set.directory)
		}
		body, exists, err := source.readOptional(set.manifest)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", set.manifest, err)
		}
		if !exists {
			return nil, fmt.Errorf("missing migration checksum manifest %s", set.manifest)
		}
		entries, err := parseManifest(set, body)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", set.manifest, err)
		}
		if err := verifyManifestFiles(source, set, files, entries); err != nil {
			return nil, err
		}
		manifests[set.manifest] = entries
	}
	return manifests, nil
}

func compareSnapshots(base, current snapshot, currentManifests map[string]manifest) error {
	for _, set := range migrationSets {
		if err := compareMigrationSet(set, base, current, currentManifests[set.manifest]); err != nil {
			return err
		}
	}
	return nil
}

func compareMigrationSet(set migrationSet, base, current snapshot, currentManifest manifest) error {
	baseFiles, err := base.listSQL(set.directory)
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

	baseManifestBody, exists, err := base.readOptional(set.manifest)
	if err != nil {
		return fmt.Errorf("read released %s: %w", set.manifest, err)
	}
	if !exists {
		return nil
	}
	baseManifest, err := parseManifest(set, baseManifestBody)
	if err != nil {
		return fmt.Errorf("parse released %s: %w", set.manifest, err)
	}
	for filePath, baseDigest := range baseManifest {
		currentDigest, ok := currentManifest[filePath]
		if !ok || currentDigest != baseDigest {
			return fmt.Errorf("released checksum entry for %s was modified or removed", filePath)
		}
	}
	return nil
}

type releaseBoundary struct {
	tag     string
	commit  string
	version [3]uint64
}

func latestReleaseBoundary(root, prefix string) (releaseBoundary, error) {
	command := exec.CommandContext(
		context.Background(),
		"git",
		"-C",
		root,
		"tag",
		"--list",
		prefix+"*",
	)
	output, err := command.Output()
	if err != nil {
		return releaseBoundary{}, fmt.Errorf("list %s release tags: %w", prefix, err)
	}
	var latest releaseBoundary
	for _, tag := range strings.Fields(string(output)) {
		version, err := parseReleaseTag(tag, prefix)
		if err != nil {
			return releaseBoundary{}, err
		}
		if latest.tag == "" || compareReleaseVersions(version, latest.version) > 0 {
			latest = releaseBoundary{tag: tag, version: version}
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
		"refs/tags/"+latest.tag+"^{commit}",
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

func verifyManifestFiles(source snapshot, set migrationSet, files []string, entries manifest) error {
	if len(files) != len(entries) {
		return fmt.Errorf(
			"%s describes %d migrations, but %s contains %d",
			set.manifest,
			len(entries),
			set.directory,
			len(files),
		)
	}
	for _, filePath := range files {
		want, ok := entries[filePath]
		if !ok {
			return fmt.Errorf("%s has no checksum for %s", set.manifest, filePath)
		}
		body, err := source.readFile(filePath)
		if err != nil {
			return fmt.Errorf("read migration %s: %w", filePath, err)
		}
		got := sha256.Sum256(body)
		if got != want {
			return fmt.Errorf("migration %s does not match its committed checksum", filePath)
		}
	}
	return nil
}

func parseManifest(set migrationSet, body []byte) (manifest, error) {
	if len(body) == 0 || body[len(body)-1] != '\n' {
		return nil, errors.New("manifest must be non-empty and end with one newline")
	}
	lines := strings.Split(string(body[:len(body)-1]), "\n")
	if len(lines) == 0 || lines[len(lines)-1] == "" {
		return nil, errors.New("manifest must not contain blank lines")
	}
	entries := make(manifest, len(lines))
	previousPath := ""
	for number, line := range lines {
		if len(line) < sha256.Size*2+3 || line[sha256.Size*2:sha256.Size*2+2] != "  " {
			return nil, fmt.Errorf("line %d must be '<lowercase sha256>  <path>'", number+1)
		}
		digestText := line[:sha256.Size*2]
		if digestText != strings.ToLower(digestText) {
			return nil, fmt.Errorf("line %d checksum must be lowercase", number+1)
		}
		digestBytes, err := hex.DecodeString(digestText)
		if err != nil || len(digestBytes) != sha256.Size {
			return nil, fmt.Errorf("line %d has an invalid SHA-256 checksum", number+1)
		}
		filePath := line[sha256.Size*2+2:]
		if err := validateManifestPath(set, filePath); err != nil {
			return nil, fmt.Errorf("line %d: %w", number+1, err)
		}
		if filePath <= previousPath {
			return nil, fmt.Errorf("line %d paths must be unique and sorted", number+1)
		}
		previousPath = filePath
		var digest [sha256.Size]byte
		copy(digest[:], digestBytes)
		entries[filePath] = digest
	}
	return entries, nil
}

func validateManifestPath(set migrationSet, filePath string) error {
	if filePath == "" || filepath.ToSlash(filePath) != filePath || path.Clean(filePath) != filePath {
		return fmt.Errorf("migration path %q is not canonical", filePath)
	}
	if path.Dir(filePath) != set.directory || path.Ext(filePath) != ".sql" {
		return fmt.Errorf("migration path %q is outside %s", filePath, set.directory)
	}
	base := path.Base(filePath)
	if len(base) < 12 || base[6] != '_' || !allDecimal(base[:6]) || base[7:len(base)-4] == "" {
		return fmt.Errorf("migration path %q does not use NNNNNN_name.sql", filePath)
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

func renderManifest(files map[string][]byte) []byte {
	paths := make([]string, 0, len(files))
	for filePath := range files {
		paths = append(paths, filePath)
	}
	sort.Strings(paths)
	var output strings.Builder
	for _, filePath := range paths {
		digest := sha256.Sum256(files[filePath])
		_, _ = fmt.Fprintf(&output, "%x  %s\n", digest, filePath)
	}
	return []byte(output.String())
}

func updateRepository(root string) error {
	source := worktreeSnapshot{root: root}
	// The manifest describes the current tree; release comparison decides which
	// entries are immutable. This lets an unreleased suffix be edited and rehashed.
	type update struct {
		path string
		body []byte
	}
	updates := make([]update, 0, len(migrationSets))
	for _, set := range migrationSets {
		paths, err := source.listSQL(set.directory)
		if err != nil {
			return fmt.Errorf("list %s: %w", set.directory, err)
		}
		files := make(map[string][]byte, len(paths))
		for _, filePath := range paths {
			body, err := source.readFile(filePath)
			if err != nil {
				return fmt.Errorf("read %s: %w", filePath, err)
			}
			files[filePath] = body
		}
		updates = append(updates, update{path: set.manifest, body: renderManifest(files)})
	}
	for _, item := range updates {
		if err := writeAtomically(filepath.Join(root, filepath.FromSlash(item.path)), item.body); err != nil {
			return fmt.Errorf("write %s: %w", item.path, err)
		}
	}
	return nil
}

func writeAtomically(target string, body []byte) error {
	temporary, err := os.CreateTemp(filepath.Dir(target), ".checksums-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() { _ = os.Remove(temporaryPath) }()
	if err := temporary.Chmod(0o644); err != nil {
		_ = temporary.Close()
		return err
	}
	if _, err := temporary.Write(body); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Sync(); err != nil {
		_ = temporary.Close()
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	return os.Rename(temporaryPath, target)
}

type worktreeSnapshot struct{ root string }

func (snapshot worktreeSnapshot) listSQL(directory string) ([]string, error) {
	entries, err := os.ReadDir(filepath.Join(snapshot.root, filepath.FromSlash(directory)))
	if err != nil {
		return nil, err
	}
	var files []string
	for _, entry := range entries {
		if path.Ext(entry.Name()) != ".sql" {
			continue
		}
		if entry.Type()&fs.ModeSymlink != 0 || !entry.Type().IsRegular() {
			return nil, fmt.Errorf("%s/%s must be a regular file", directory, entry.Name())
		}
		files = append(files, path.Join(directory, entry.Name()))
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

func (snapshot gitSnapshot) listSQL(directory string) ([]string, error) {
	command := snapshot.command("ls-tree", "-r", "-z", "--name-only", snapshot.ref, "--", directory)
	output, err := command.Output()
	if err != nil {
		return nil, err
	}
	var files []string
	for _, candidate := range bytes.Split(output, []byte{0}) {
		filePath := string(candidate)
		if filePath != "" && path.Dir(filePath) == directory && path.Ext(filePath) == ".sql" {
			files = append(files, filePath)
		}
	}
	sort.Strings(files)
	return files, nil
}

func (snapshot gitSnapshot) readFile(filePath string) ([]byte, error) {
	command := snapshot.command("cat-file", "blob", snapshot.ref+":"+filePath)
	return command.Output()
}

func (snapshot gitSnapshot) readOptional(filePath string) ([]byte, bool, error) {
	command := snapshot.command("ls-tree", "--name-only", snapshot.ref, "--", filePath)
	output, err := command.Output()
	if err != nil {
		return nil, false, err
	}
	if strings.TrimSpace(string(output)) != filePath {
		return nil, false, nil
	}
	body, err := snapshot.readFile(filePath)
	return body, err == nil, err
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
