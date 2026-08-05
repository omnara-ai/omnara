package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonversion"
	releasetool "github.com/omnara-ai/omnara/tools/internal/release"
)

const (
	checksumsFilename = "SHA256SUMS"
	stableChannelTag  = "omnarad-stable"
)

var (
	artifactFilenames = []string{
		"omnarad-darwin-amd64",
		"omnarad-darwin-arm64",
		"omnarad-linux-amd64",
		"omnarad-linux-arm64",
	}
	platforms = map[string]platform{
		"darwin-amd64": {goos: "darwin", goarch: "amd64"},
		"darwin-arm64": {goos: "darwin", goarch: "arm64"},
		"linux-amd64":  {goos: "linux", goarch: "amd64"},
		"linux-arm64":  {goos: "linux", goarch: "arm64"},
	}
)

type platform struct {
	goos   string
	goarch string
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: omnarad-release <resolve|build|generate|verify|promote>")
	}

	switch args[0] {
	case "resolve":
		if len(args) != 1 {
			return errors.New("resolve: unexpected arguments")
		}
		return resolve()
	case "build":
		if len(args) != 1 {
			return errors.New("build: unexpected arguments")
		}
		return build()
	case "generate", "verify":
		version, err := requiredReleaseVersion()
		if err != nil {
			return err
		}
		directory, err := parseDirectory(args[0], args[1:])
		if err != nil {
			return err
		}
		releaseDownloads, err := releaseDownloadBaseURL()
		if err != nil {
			return err
		}
		if args[0] == "generate" {
			return generate(directory, version, releaseDownloads)
		}
		return verify(directory, version, releaseDownloads)
	case "promote":
		version, err := requiredReleaseVersion()
		if err != nil {
			return err
		}
		directory, err := parseDirectory(args[0], args[1:])
		if err != nil {
			return err
		}
		releaseTag, err := requiredEnvironment("GITHUB_REF_NAME")
		if err != nil {
			return err
		}
		releaseSHA, err := requiredEnvironment("RELEASE_SHA")
		if err != nil {
			return err
		}
		releaseDownloads, err := releaseDownloadBaseURL()
		if err != nil {
			return err
		}
		return promote(directory, version, releaseTag, releaseSHA, releaseDownloads)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func resolve() error {
	version, err := resolveVersion(
		os.Getenv("GITHUB_EVENT_NAME"),
		os.Getenv("GITHUB_REF_NAME"),
		os.Getenv("GITHUB_REF"),
		os.Getenv("INPUT_VERSION"),
	)
	if err != nil {
		return err
	}

	if err := runExternal(
		"git",
		"fetch",
		"--no-tags",
		"origin",
		"+refs/heads/main:refs/remotes/origin/main",
	); err != nil {
		return err
	}
	releaseSHA, err := releasetool.ResolveSourceCommit(context.Background(), ".", "origin/main")
	if err != nil {
		return err
	}

	return releasetool.AppendGitHubOutputs(
		os.Getenv("GITHUB_OUTPUT"),
		releasetool.Output{Name: "version", Value: version},
		releasetool.Output{Name: "release_sha", Value: releaseSHA},
	)
}

func resolveVersion(event string, refName string, ref string, inputVersion string) (string, error) {
	var version string
	switch event {
	case "push":
		if !strings.HasPrefix(refName, "omnarad-v") {
			return "", errors.New("release tag must be omnarad-vMAJOR.MINOR.PATCH")
		}
		version = strings.TrimPrefix(refName, "omnarad-v")
	case "workflow_dispatch":
		if ref != "refs/heads/main" {
			return "", errors.New("manual release validation must run from main")
		}
		version = inputVersion
	default:
		return "", fmt.Errorf("unsupported release event %q", event)
	}
	if _, err := daemonversion.ParseRelease(version); err != nil {
		return "", errors.New("version must be MAJOR.MINOR.PATCH")
	}
	return version, nil
}

func build() error {
	version, err := requiredEnvironment("OMNARAD_VERSION")
	if err != nil {
		return err
	}
	if _, err := daemonversion.ParseRelease(version); err != nil {
		return errors.New("OMNARAD_VERSION must be MAJOR.MINOR.PATCH")
	}
	platformName, err := requiredEnvironment("PLATFORM")
	if err != nil {
		return err
	}
	configuration, ok := platforms[platformName]
	if !ok {
		return fmt.Errorf("unsupported platform %q", platformName)
	}
	if runtime.GOOS != configuration.goos || runtime.GOARCH != configuration.goarch {
		return fmt.Errorf(
			"%s must be built natively on %s-%s",
			platformName,
			configuration.goos,
			configuration.goarch,
		)
	}

	if err := runExternal("make", "build-omnarad", "OMNARAD_VERSION="+version); err != nil {
		return err
	}
	actualVersion, err := externalOutput(filepath.Join("bin", "omnarad"), "--version")
	if err != nil {
		return err
	}
	if strings.TrimSpace(actualVersion) != version {
		return fmt.Errorf("built omnarad version is %q, want %q", strings.TrimSpace(actualVersion), version)
	}
	if err := os.Mkdir("release", 0o755); err != nil && !errors.Is(err, os.ErrExist) {
		return fmt.Errorf("create release directory: %w", err)
	}
	destination := filepath.Join("release", "omnarad-"+platformName)
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("release artifact already exists: %s", destination)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect release artifact: %w", err)
	}
	if err := os.Rename(filepath.Join("bin", "omnarad"), destination); err != nil {
		return fmt.Errorf("move release artifact: %w", err)
	}
	return nil
}

func parseDirectory(command string, args []string) (string, error) {
	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	directory := flags.String("dir", "", "")
	if err := flags.Parse(args); err != nil {
		return "", fmt.Errorf("%s arguments: %w", command, err)
	}
	if flags.NArg() != 0 {
		return "", fmt.Errorf("%s: unexpected positional arguments", command)
	}
	if *directory == "" {
		return "", errors.New("release directory is required")
	}
	return *directory, nil
}

func generate(directory string, version string, releaseDownloads string) error {
	if _, err := daemonversion.ParseRelease(version); err != nil {
		return errors.New("OMNARAD_VERSION must be MAJOR.MINOR.PATCH")
	}
	if err := validateDirectory(directory, false); err != nil {
		return err
	}
	hashes, err := generateArtifactHashes(directory)
	if err != nil {
		return err
	}
	checksums := generateChecksums(hashes)
	if err := os.WriteFile(filepath.Join(directory, checksumsFilename), checksums, 0o644); err != nil {
		return fmt.Errorf("write %s: %w", checksumsFilename, err)
	}
	for _, artifact := range artifactFilenames {
		name := manifestFilename(artifact)
		contents := generateManifest(version, artifact, hashes[artifact], releaseDownloads)
		if err := os.WriteFile(filepath.Join(directory, name), contents, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", name, err)
		}
	}
	return verify(directory, version, releaseDownloads)
}

func verify(directory string, version string, releaseDownloads string) error {
	if err := validateDirectory(directory, true); err != nil {
		return err
	}
	if _, err := daemonversion.ParseRelease(version); err != nil {
		return errors.New("OMNARAD_VERSION must be MAJOR.MINOR.PATCH")
	}
	hashes, err := generateArtifactHashes(directory)
	if err != nil {
		return err
	}
	expected := generateChecksums(hashes)
	actual, err := os.ReadFile(filepath.Join(directory, checksumsFilename))
	if err != nil {
		return fmt.Errorf("read %s: %w", checksumsFilename, err)
	}
	if !bytes.Equal(actual, expected) {
		return fmt.Errorf("%s does not match the release artifacts", checksumsFilename)
	}
	for _, artifact := range artifactFilenames {
		name := manifestFilename(artifact)
		actual, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		expected := generateManifest(version, artifact, hashes[artifact], releaseDownloads)
		if !bytes.Equal(actual, expected) {
			return fmt.Errorf("%s does not match the release artifacts", name)
		}
	}
	return nil
}

func promote(
	directory string,
	version string,
	releaseTag string,
	releaseSHA string,
	releaseDownloads string,
) error {
	if releaseTag != "omnarad-v"+version {
		return errors.New("GITHUB_REF_NAME does not match OMNARAD_VERSION")
	}
	if err := releasetool.ValidateCommit(releaseSHA); err != nil {
		return fmt.Errorf("RELEASE_SHA: %w", err)
	}
	if err := verify(directory, version, releaseDownloads); err != nil {
		return err
	}
	publishedReleaseTags, err := externalOutput(
		"gh",
		"api",
		"--paginate",
		"repos/{owner}/{repo}/releases?per_page=100",
		"--jq",
		".[] | select(.draft == false and .prerelease == false) | .tag_name",
	)
	if err != nil {
		return err
	}
	if err := checkPromotion(publishedReleaseTags, version); err != nil {
		return err
	}

	manifests := manifestPaths(directory)
	if releaseTagExists(publishedReleaseTags, stableChannelTag) {
		arguments := append([]string{"release", "upload", stableChannelTag}, manifests...)
		arguments = append(arguments, "--clobber")
		if err := runExternal("gh", arguments...); err != nil {
			return err
		}
	} else {
		arguments := append([]string{"release", "create", stableChannelTag}, manifests...)
		arguments = append(
			arguments,
			"--latest=false",
			"--notes",
			"Mutable platform manifests for the current stable omnarad release.",
			"--target",
			releaseSHA,
			"--title",
			"omnarad stable channel",
		)
		if err := runExternal("gh", arguments...); err != nil {
			return err
		}
	}

	for _, artifact := range artifactFilenames {
		name := manifestFilename(artifact)
		published, err := externalOutput(
			"gh",
			"release",
			"download",
			stableChannelTag,
			"--pattern",
			name,
			"--output",
			"-",
		)
		if err != nil {
			return err
		}
		source, err := os.ReadFile(filepath.Join(directory, name))
		if err != nil {
			return fmt.Errorf("read source channel manifest %s: %w", name, err)
		}
		if !bytes.Equal([]byte(published), source) {
			return fmt.Errorf("published channel manifest %s does not match the release", name)
		}
	}
	return runExternal(
		"gh",
		"api",
		"--method",
		"PATCH",
		"repos/{owner}/{repo}/git/refs/tags/"+stableChannelTag,
		"-f",
		"sha="+releaseSHA,
		"-F",
		"force=true",
	)
}

func checkPromotion(publishedReleaseTags string, version string) error {
	target, err := daemonversion.ParseRelease(version)
	if err != nil {
		return errors.New("OMNARAD_VERSION must be MAJOR.MINOR.PATCH")
	}
	targetTag := "omnarad-v" + version
	targetFound := false
	for _, tag := range strings.Split(publishedReleaseTags, "\n") {
		tag = strings.TrimSpace(tag)
		if !strings.HasPrefix(tag, "omnarad-v") {
			continue
		}
		releasedVersion := strings.TrimPrefix(tag, "omnarad-v")
		released, err := daemonversion.ParseRelease(releasedVersion)
		if err != nil {
			return fmt.Errorf("published omnarad release tag %q is invalid", tag)
		}
		if tag == targetTag {
			targetFound = true
		}
		if daemonversion.Compare(released, target) > 0 {
			return fmt.Errorf(
				"refusing to promote omnarad-stable to %s while newer release %s is published",
				version,
				releasedVersion,
			)
		}
	}
	if !targetFound {
		return fmt.Errorf("published release %s was not found", targetTag)
	}
	return nil
}

func releaseTagExists(publishedReleaseTags string, target string) bool {
	for _, tag := range strings.Split(publishedReleaseTags, "\n") {
		if strings.TrimSpace(tag) == target {
			return true
		}
	}
	return false
}

func validateDirectory(directory string, requireBundle bool) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect release directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("release directory must be a real directory")
	}

	allowed := make(map[string]struct{}, len(artifactFilenames)*2+1)
	for _, name := range artifactFilenames {
		allowed[name] = struct{}{}
		allowed[manifestFilename(name)] = struct{}{}
	}
	allowed[checksumsFilename] = struct{}{}

	entries, err := os.ReadDir(directory)
	if err != nil {
		return fmt.Errorf("read release directory: %w", err)
	}
	found := make(map[string]bool, len(entries))
	for _, entry := range entries {
		if _, ok := allowed[entry.Name()]; !ok {
			return fmt.Errorf("unexpected release artifact %s", entry.Name())
		}
		entryInfo, err := os.Lstat(filepath.Join(directory, entry.Name()))
		if err != nil {
			return fmt.Errorf("inspect release artifact %s: %w", entry.Name(), err)
		}
		if !entryInfo.Mode().IsRegular() || entryInfo.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("release artifact %s must be a regular file", entry.Name())
		}
		found[entry.Name()] = true
	}

	for _, name := range artifactFilenames {
		if !found[name] {
			return fmt.Errorf("missing release artifact %s", name)
		}
	}
	if requireBundle && !found[checksumsFilename] {
		return fmt.Errorf("missing release artifact %s", checksumsFilename)
	}
	if requireBundle {
		for _, artifact := range artifactFilenames {
			name := manifestFilename(artifact)
			if !found[name] {
				return fmt.Errorf("missing release artifact %s", name)
			}
		}
	}
	return nil
}

func generateArtifactHashes(directory string) (map[string]string, error) {
	hashes := make(map[string]string, len(artifactFilenames))
	for _, name := range artifactFilenames {
		hash, err := hashFile(filepath.Join(directory, name))
		if err != nil {
			return nil, fmt.Errorf("hash %s: %w", name, err)
		}
		hashes[name] = hash
	}
	return hashes, nil
}

func generateChecksums(hashes map[string]string) []byte {
	var checksums bytes.Buffer
	for _, name := range artifactFilenames {
		_, _ = fmt.Fprintf(&checksums, "%s  %s\n", hashes[name], name)
	}
	return checksums.Bytes()
}

func manifestFilename(artifact string) string {
	return strings.TrimPrefix(artifact, "omnarad-") + ".txt"
}

func manifestPaths(directory string) []string {
	paths := make([]string, 0, len(artifactFilenames))
	for _, artifact := range artifactFilenames {
		paths = append(paths, filepath.Join(directory, manifestFilename(artifact)))
	}
	return paths
}

func generateManifest(version string, artifact string, hash string, releaseDownloads string) []byte {
	return []byte(fmt.Sprintf(
		"version=%s\nurl=%s/omnarad-v%s/%s\nsha256=%s\n",
		version,
		releaseDownloads,
		version,
		artifact,
		hash,
	))
}

func hashFile(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() {
		_ = file.Close()
	}()

	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func requiredEnvironment(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func requiredReleaseVersion() (string, error) {
	version, err := requiredEnvironment("OMNARAD_VERSION")
	if err != nil {
		return "", err
	}
	if _, err := daemonversion.ParseRelease(version); err != nil {
		return "", errors.New("OMNARAD_VERSION must be MAJOR.MINOR.PATCH")
	}
	return version, nil
}

func releaseDownloadBaseURL() (string, error) {
	serverURL, err := requiredEnvironment("GITHUB_SERVER_URL")
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(serverURL)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil ||
		parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", errors.New("GITHUB_SERVER_URL must be an HTTPS server URL")
	}
	repository, err := requiredEnvironment("GITHUB_REPOSITORY")
	if err != nil {
		return "", err
	}
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" ||
		url.PathEscape(parts[0]) != parts[0] || url.PathEscape(parts[1]) != parts[1] {
		return "", errors.New("GITHUB_REPOSITORY must be OWNER/REPOSITORY")
	}
	return strings.TrimRight(serverURL, "/") + "/" + repository + "/releases/download", nil
}

func runExternal(name string, args ...string) error {
	command := exec.CommandContext(context.Background(), name, args...)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func externalOutput(name string, args ...string) (string, error) {
	command := exec.CommandContext(context.Background(), name, args...)
	var stdout bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return "", fmt.Errorf("%s failed: %w", name, err)
	}
	return stdout.String(), nil
}
