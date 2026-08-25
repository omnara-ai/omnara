package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonversion"
	releasetool "github.com/omnara-ai/omnara/tools/internal/release"
)

const (
	manifestFilename = "cluster-release.json"
	notesFilename    = "release-notes.md"
	imageWeb         = "web"
	imageAPI         = "api"
	imageWorker      = "worker"
	imageMaintenance = "maintenance"
	imageMigrations  = "migrations"
	imageMCPRegistry = "mcp-registry"
)

var (
	digestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)
	images        = []imageDefinition{
		{Name: imageWeb, Repository: "ghcr.io/omnara-ai/omnara-web"},
		{Name: imageAPI, Repository: "ghcr.io/omnara-ai/omnara-api"},
		{Name: imageWorker, Repository: "ghcr.io/omnara-ai/omnara-worker"},
		{Name: imageMaintenance, Repository: "ghcr.io/omnara-ai/omnara-maintenance"},
		{Name: imageMigrations, Repository: "ghcr.io/omnara-ai/omnara-migrations"},
		{Name: imageMCPRegistry, Repository: "ghcr.io/omnara-ai/omnara-mcp-registry"},
	}
)

type imageDefinition struct {
	Name       string
	Repository string
}

type digestRecord struct {
	Name         string `json:"name"`
	Version      string `json:"version"`
	SourceCommit string `json:"source_commit"`
	Reference    string `json:"reference"`
}

type manifestImages struct {
	Web         string `json:"web"`
	API         string `json:"api"`
	Worker      string `json:"worker"`
	Maintenance string `json:"maintenance"`
	Migrations  string `json:"migrations"`
	MCPRegistry string `json:"mcp_registry"`
}

type clusterManifest struct {
	Version      string         `json:"version"`
	SourceCommit string         `json:"source_commit"`
	Images       manifestImages `json:"images"`
}

type imageInspector func(context.Context, string) (string, error)

func main() {
	if err := run(os.Args[1:]); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("usage: cluster-release <resolve|check-tag|record|generate|verify-public>")
	}
	switch args[0] {
	case "resolve":
		if len(args) != 1 {
			return errors.New("resolve: unexpected arguments")
		}
		return resolve()
	case "check-tag":
		if len(args) != 1 {
			return errors.New("check-tag: unexpected arguments")
		}
		return checkTagFromEnvironment(context.Background())
	case "record":
		directory, err := requiredFlag("record", "dir", args[1:])
		if err != nil {
			return err
		}
		return recordFromEnvironment(directory)
	case "generate":
		directory, err := requiredFlag("generate", "dir", args[1:])
		if err != nil {
			return err
		}
		return generateFromEnvironment(directory)
	case "verify-public":
		manifestPath, err := requiredFlag("verify-public", "manifest", args[1:])
		if err != nil {
			return err
		}
		return verifyPublic(context.Background(), manifestPath, inspectImage)
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

func resolve() error {
	version, err := resolveVersion(os.Getenv("GITHUB_EVENT_NAME"), os.Getenv("GITHUB_REF_NAME"))
	if err != nil {
		return err
	}
	sourceCommit, err := releasetool.ResolveSourceCommit(
		context.Background(),
		".",
		"refs/remotes/origin/main",
	)
	if err != nil {
		return err
	}
	return releasetool.AppendGitHubOutputs(
		os.Getenv("GITHUB_OUTPUT"),
		releasetool.Output{Name: "version", Value: version},
		releasetool.Output{Name: "source_commit", Value: sourceCommit},
	)
}

func resolveVersion(event string, refName string) (string, error) {
	if event != "push" {
		return "", fmt.Errorf("unsupported release event %q", event)
	}
	if !strings.HasPrefix(refName, "cluster-v") {
		return "", errors.New("release tag must be cluster-vMAJOR.MINOR.PATCH")
	}
	version := strings.TrimPrefix(refName, "cluster-")
	if err := validateVersion(version); err != nil {
		return "", errors.New("release tag must be cluster-vMAJOR.MINOR.PATCH")
	}
	return version, nil
}

func validateVersion(version string) error {
	if !strings.HasPrefix(version, "v") {
		return errors.New("version must be vMAJOR.MINOR.PATCH")
	}
	if _, err := daemonversion.ParseRelease(strings.TrimPrefix(version, "v")); err != nil {
		return errors.New("version must be vMAJOR.MINOR.PATCH")
	}
	return nil
}

func checkTagFromEnvironment(ctx context.Context) error {
	name, err := requiredEnvironment("NAME")
	if err != nil {
		return err
	}
	image, err := requiredEnvironment("IMAGE")
	if err != nil {
		return err
	}
	version, err := requiredEnvironment("VERSION")
	if err != nil {
		return err
	}
	return checkTag(ctx, name, image, version, inspectImage)
}

func checkTag(
	ctx context.Context,
	name string,
	image string,
	version string,
	inspect imageInspector,
) error {
	definition, err := validateImage(name, image)
	if err != nil {
		return err
	}
	if err := validateVersion(version); err != nil {
		return err
	}
	reference := definition.Repository + ":" + version
	output, inspectErr := inspect(ctx, reference)
	if inspectErr == nil {
		return fmt.Errorf(
			"%s already exists; if it is from an unpublished partial release, delete only that package version before rerunning",
			reference,
		)
	}
	lower := strings.ToLower(output)
	if strings.Contains(lower, "manifest unknown") || strings.Contains(lower, "not found") {
		return nil
	}
	return fmt.Errorf("inspect %s: %w: %s", reference, inspectErr, strings.TrimSpace(output))
}

func recordFromEnvironment(directory string) error {
	name, err := requiredEnvironment("NAME")
	if err != nil {
		return err
	}
	image, err := requiredEnvironment("IMAGE")
	if err != nil {
		return err
	}
	digest, err := requiredEnvironment("DIGEST")
	if err != nil {
		return err
	}
	version, err := requiredEnvironment("VERSION")
	if err != nil {
		return err
	}
	sourceCommit, err := requiredEnvironment("SOURCE_COMMIT")
	if err != nil {
		return err
	}
	record, err := newDigestRecord(name, image, digest, version, sourceCommit)
	if err != nil {
		return err
	}
	return writeDigestRecord(directory, record)
}

func newDigestRecord(
	name string,
	image string,
	digest string,
	version string,
	sourceCommit string,
) (digestRecord, error) {
	definition, err := validateImage(name, image)
	if err != nil {
		return digestRecord{}, err
	}
	if err := validateVersion(version); err != nil {
		return digestRecord{}, err
	}
	if err := releasetool.ValidateCommit(sourceCommit); err != nil {
		return digestRecord{}, err
	}
	if !digestPattern.MatchString(digest) {
		return digestRecord{}, errors.New("image digest must be sha256 followed by 64 lowercase hex characters")
	}
	return digestRecord{
		Name:         definition.Name,
		Version:      version,
		SourceCommit: sourceCommit,
		Reference:    definition.Repository + "@" + digest,
	}, nil
}

func writeDigestRecord(directory string, record digestRecord) error {
	if err := ensureRealDirectory(directory); err != nil {
		return err
	}
	data, err := encodeJSON(record)
	if err != nil {
		return err
	}
	return writeExclusive(filepath.Join(directory, record.Name+".json"), data)
}

func generateFromEnvironment(directory string) error {
	version, err := resolveVersion(os.Getenv("GITHUB_EVENT_NAME"), os.Getenv("GITHUB_REF_NAME"))
	if err != nil {
		return err
	}
	manifest, err := generate(directory, version, manifestFilename, notesFilename)
	if err != nil {
		return err
	}
	return releasetool.AppendGitHubOutputs(
		os.Getenv("GITHUB_OUTPUT"),
		releasetool.Output{Name: "version", Value: manifest.Version},
	)
}

func generate(
	directory string,
	version string,
	manifestPath string,
	notesPath string,
) (clusterManifest, error) {
	if err := validateVersion(version); err != nil {
		return clusterManifest{}, err
	}
	records, err := readDigestRecords(directory, version)
	if err != nil {
		return clusterManifest{}, err
	}
	sourceCommit := records[images[0].Name].SourceCommit
	for _, definition := range images[1:] {
		if records[definition.Name].SourceCommit != sourceCommit {
			return clusterManifest{}, errors.New("digest records disagree on source commit")
		}
	}
	manifest := clusterManifest{
		Version:      version,
		SourceCommit: sourceCommit,
		Images: manifestImages{
			Web:         records[imageWeb].Reference,
			API:         records[imageAPI].Reference,
			Worker:      records[imageWorker].Reference,
			Maintenance: records[imageMaintenance].Reference,
			Migrations:  records[imageMigrations].Reference,
			MCPRegistry: records[imageMCPRegistry].Reference,
		},
	}
	if err := validateManifest(manifest); err != nil {
		return clusterManifest{}, err
	}
	manifestData, err := encodeJSON(manifest)
	if err != nil {
		return clusterManifest{}, err
	}
	if err := writeExclusive(manifestPath, manifestData); err != nil {
		return clusterManifest{}, err
	}
	if err := writeExclusive(notesPath, []byte(releaseNotes(manifest))); err != nil {
		return clusterManifest{}, err
	}
	return manifest, nil
}

func readDigestRecords(directory string, version string) (map[string]digestRecord, error) {
	if err := validateRealDirectory(directory); err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return nil, fmt.Errorf("read digest directory: %w", err)
	}
	expected := make(map[string]imageDefinition, len(images))
	for _, definition := range images {
		expected[definition.Name+".json"] = definition
	}
	if len(entries) != len(expected) {
		return nil, fmt.Errorf("digest directory must contain exactly %d records", len(expected))
	}
	records := make(map[string]digestRecord, len(images))
	for _, entry := range entries {
		definition, ok := expected[entry.Name()]
		if !ok {
			return nil, fmt.Errorf("unexpected digest record %s", entry.Name())
		}
		path := filepath.Join(directory, entry.Name())
		data, err := readRegularFile(path)
		if err != nil {
			return nil, err
		}
		var record digestRecord
		if err := decodeStrict(data, &record); err != nil {
			return nil, fmt.Errorf("decode digest record %s: %w", entry.Name(), err)
		}
		if err := validateDigestRecord(record, definition, version); err != nil {
			return nil, fmt.Errorf("invalid digest record %s: %w", entry.Name(), err)
		}
		records[definition.Name] = record
	}
	return records, nil
}

func validateDigestRecord(
	record digestRecord,
	definition imageDefinition,
	version string,
) error {
	if record.Name != definition.Name {
		return fmt.Errorf("name is %q, want %q", record.Name, definition.Name)
	}
	if record.Version != version {
		return fmt.Errorf("version is %q, want %q", record.Version, version)
	}
	if err := releasetool.ValidateCommit(record.SourceCommit); err != nil {
		return err
	}
	prefix := definition.Repository + "@"
	if !strings.HasPrefix(record.Reference, prefix) {
		return fmt.Errorf("reference must start with %s", prefix)
	}
	if !digestPattern.MatchString(strings.TrimPrefix(record.Reference, prefix)) {
		return errors.New("reference must contain a canonical sha256 digest")
	}
	return nil
}

func releaseNotes(manifest clusterManifest) string {
	var notes strings.Builder
	_, _ = fmt.Fprintf(
		&notes,
		"Canonical cluster %s images built from `%s`.\n\n",
		manifest.Version,
		manifest.SourceCommit,
	)
	for _, definition := range images {
		_, _ = fmt.Fprintf(&notes, "- `%s`\n", manifestReference(manifest, definition.Name))
	}
	return notes.String()
}

func verifyPublic(
	ctx context.Context,
	manifestPath string,
	inspect imageInspector,
) error {
	manifest, err := readManifest(manifestPath)
	if err != nil {
		return err
	}
	for _, definition := range images {
		reference := manifestReference(manifest, definition.Name)
		output, inspectErr := inspect(ctx, reference)
		if inspectErr != nil {
			return fmt.Errorf(
				"inspect %s: %w: %s",
				reference,
				inspectErr,
				strings.TrimSpace(output),
			)
		}
	}
	return nil
}

func readManifest(path string) (clusterManifest, error) {
	data, err := readRegularFile(path)
	if err != nil {
		return clusterManifest{}, err
	}
	var manifest clusterManifest
	if err := decodeStrict(data, &manifest); err != nil {
		return clusterManifest{}, fmt.Errorf("decode cluster manifest: %w", err)
	}
	if err := validateManifest(manifest); err != nil {
		return clusterManifest{}, err
	}
	return manifest, nil
}

func validateManifest(manifest clusterManifest) error {
	if err := validateVersion(manifest.Version); err != nil {
		return err
	}
	if err := releasetool.ValidateCommit(manifest.SourceCommit); err != nil {
		return err
	}
	for _, definition := range images {
		reference := manifestReference(manifest, definition.Name)
		prefix := definition.Repository + "@"
		if !strings.HasPrefix(reference, prefix) ||
			!digestPattern.MatchString(strings.TrimPrefix(reference, prefix)) {
			return fmt.Errorf("manifest reference for %s is invalid", definition.Name)
		}
	}
	return nil
}

func manifestReference(manifest clusterManifest, name string) string {
	switch name {
	case imageWeb:
		return manifest.Images.Web
	case imageAPI:
		return manifest.Images.API
	case imageWorker:
		return manifest.Images.Worker
	case imageMaintenance:
		return manifest.Images.Maintenance
	case imageMigrations:
		return manifest.Images.Migrations
	case imageMCPRegistry:
		return manifest.Images.MCPRegistry
	default:
		return ""
	}
}

func validateImage(name string, image string) (imageDefinition, error) {
	for _, definition := range images {
		if definition.Name != name {
			continue
		}
		if image != definition.Repository {
			return imageDefinition{}, fmt.Errorf(
				"image for %s is %q, want %q",
				name,
				image,
				definition.Repository,
			)
		}
		return definition, nil
	}
	return imageDefinition{}, fmt.Errorf("unknown cluster image %q", name)
}

func inspectImage(ctx context.Context, reference string) (string, error) {
	command := exec.CommandContext(ctx, "docker", "buildx", "imagetools", "inspect", reference)
	output, err := command.CombinedOutput()
	return string(output), err
}

func requiredFlag(commandName string, flagName string, args []string) (string, error) {
	flags := flag.NewFlagSet(commandName, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	value := flags.String(flagName, "", "")
	if err := flags.Parse(args); err != nil {
		return "", fmt.Errorf("%s arguments: %w", commandName, err)
	}
	if flags.NArg() != 0 {
		return "", fmt.Errorf("%s: unexpected positional arguments", commandName)
	}
	if *value == "" {
		return "", fmt.Errorf("%s is required", flagName)
	}
	return *value, nil
}

func requiredEnvironment(name string) (string, error) {
	value := os.Getenv(name)
	if value == "" {
		return "", fmt.Errorf("%s is required", name)
	}
	return value, nil
}

func ensureRealDirectory(directory string) error {
	if err := os.MkdirAll(directory, 0o755); err != nil {
		return fmt.Errorf("create directory: %w", err)
	}
	return validateRealDirectory(directory)
}

func validateRealDirectory(directory string) error {
	info, err := os.Lstat(directory)
	if err != nil {
		return fmt.Errorf("inspect directory: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return errors.New("release directory must be a real directory")
	}
	return nil
}

func readRegularFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect %s: %w", path, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%s must be a regular file", path)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	return data, nil
}

func writeExclusive(path string, data []byte) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create %s: %w", path, err)
	}
	if _, err := file.Write(data); err != nil {
		_ = file.Close()
		return fmt.Errorf("write %s: %w", path, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", path, err)
	}
	return nil
}

func encodeJSON(value any) ([]byte, error) {
	var data bytes.Buffer
	encoder := json.NewEncoder(&data)
	encoder.SetEscapeHTML(false)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(value); err != nil {
		return nil, fmt.Errorf("encode JSON: %w", err)
	}
	return data.Bytes(), nil
}

func decodeStrict(data []byte, value any) error {
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(value); err != nil {
		return err
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("unexpected trailing JSON value")
		}
		return err
	}
	return nil
}
