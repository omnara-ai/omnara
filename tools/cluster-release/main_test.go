package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		event   string
		refName string
		want    string
		wantErr string
	}{
		{
			name:    "cluster tag",
			event:   "push",
			refName: "cluster-v1.2.3",
			want:    "v1.2.3",
		},
		{
			name:    "wrong event",
			event:   "workflow_dispatch",
			refName: "cluster-v1.2.3",
			wantErr: "unsupported release event",
		},
		{
			name:    "wrong prefix",
			event:   "push",
			refName: "v1.2.3",
			wantErr: "release tag",
		},
		{
			name:    "noncanonical",
			event:   "push",
			refName: "cluster-v01.2.3",
			wantErr: "release tag",
		},
		{
			name:    "overflowing",
			event:   "push",
			refName: "cluster-v18446744073709551616.0.0",
			wantErr: "release tag",
		},
		{
			name:    "suffix",
			event:   "push",
			refName: "cluster-v1.2.3-rc.1",
			wantErr: "release tag",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveVersion(test.event, test.refName)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("resolveVersion error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveVersion: %v", err)
			}
			if got != test.want {
				t.Fatalf("resolveVersion = %q, want %q", got, test.want)
			}
		})
	}
}

func TestCheckTag(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		imageName  string
		image      string
		version    string
		output     string
		inspectErr error
		wantErr    string
	}{
		{
			name:       "missing",
			imageName:  "api",
			image:      "ghcr.io/omnara-ai/omnara-api",
			version:    "v1.2.3",
			output:     "manifest unknown",
			inspectErr: errors.New("exit status 1"),
		},
		{
			name:      "existing",
			imageName: "api",
			image:     "ghcr.io/omnara-ai/omnara-api",
			version:   "v1.2.3",
			wantErr:   "delete only that package version",
		},
		{
			name:       "unexpected inspect failure",
			imageName:  "api",
			image:      "ghcr.io/omnara-ai/omnara-api",
			version:    "v1.2.3",
			output:     "connection refused",
			inspectErr: errors.New("exit status 1"),
			wantErr:    "connection refused",
		},
		{
			name:       "wrong repository",
			imageName:  "api",
			image:      "ghcr.io/omnara-ai/omnara-worker",
			version:    "v1.2.3",
			output:     "manifest unknown",
			inspectErr: errors.New("exit status 1"),
			wantErr:    "image for api",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			called := false
			inspect := func(_ context.Context, reference string) (string, error) {
				called = true
				wantReference := test.image + ":" + test.version
				if reference != wantReference {
					t.Fatalf("reference = %q, want %q", reference, wantReference)
				}
				return test.output, test.inspectErr
			}
			err := checkTag(
				context.Background(),
				test.imageName,
				test.image,
				test.version,
				inspect,
			)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("checkTag error = %v, want %q", err, test.wantErr)
				}
				if test.name == "wrong repository" && called {
					t.Fatal("inspector called for invalid image")
				}
				return
			}
			if err != nil {
				t.Fatalf("checkTag: %v", err)
			}
			if !called {
				t.Fatal("inspector was not called")
			}
		})
	}
}

func TestGenerateClusterRelease(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	version := "v1.2.3"
	commit := strings.Repeat("a", 40)
	records := writeValidRecords(t, directory, version, commit)
	output := t.TempDir()
	manifestPath := filepath.Join(output, manifestFilename)
	notesPath := filepath.Join(output, notesFilename)

	manifest, err := generate(directory, version, manifestPath, notesPath)
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if manifest.Version != version || manifest.SourceCommit != commit {
		t.Fatalf("manifest metadata = %+v", manifest)
	}
	for _, definition := range images {
		if got := manifestReference(manifest, definition.Name); got != records[definition.Name].Reference {
			t.Fatalf("%s reference = %q, want %q", definition.Name, got, records[definition.Name].Reference)
		}
	}

	decoded, err := readManifest(manifestPath)
	if err != nil {
		t.Fatalf("read manifest: %v", err)
	}
	if decoded != manifest {
		t.Fatalf("read manifest = %+v, want %+v", decoded, manifest)
	}
	notes, err := os.ReadFile(notesPath)
	if err != nil {
		t.Fatalf("read notes: %v", err)
	}
	if string(notes) != releaseNotes(manifest) {
		t.Fatalf("release notes = %q, want %q", notes, releaseNotes(manifest))
	}

	var raw map[string]any
	manifestData, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatalf("read manifest data: %v", err)
	}
	if err := json.Unmarshal(manifestData, &raw); err != nil {
		t.Fatalf("decode manifest JSON: %v", err)
	}
	if len(raw) != 3 {
		t.Fatalf("manifest has %d top-level fields, want 3", len(raw))
	}
}

func TestGenerateRejectsInvalidDigestRecords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		prepare func(*testing.T, string)
		want    string
	}{
		{
			name: "missing",
			prepare: func(t *testing.T, directory string) {
				t.Helper()
				if err := os.Remove(filepath.Join(directory, "web.json")); err != nil {
					t.Fatalf("remove record: %v", err)
				}
			},
			want: "exactly 5 records",
		},
		{
			name: "unexpected",
			prepare: func(t *testing.T, directory string) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(directory, "other.json"), []byte("{}\n"), 0o600); err != nil {
					t.Fatalf("write record: %v", err)
				}
			},
			want: "exactly 5 records",
		},
		{
			name: "symlinked",
			prepare: func(t *testing.T, directory string) {
				t.Helper()
				path := filepath.Join(directory, "web.json")
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove record: %v", err)
				}
				if err := os.Symlink(filepath.Join(directory, "api.json"), path); err != nil {
					t.Fatalf("symlink record: %v", err)
				}
			},
			want: "must be a regular file",
		},
		{
			name: "different commit",
			prepare: func(t *testing.T, directory string) {
				t.Helper()
				record := readRecord(t, filepath.Join(directory, "web.json"))
				record.SourceCommit = strings.Repeat("b", 40)
				replaceRecord(t, filepath.Join(directory, "web.json"), record)
			},
			want: "disagree on source commit",
		},
		{
			name: "wrong reference",
			prepare: func(t *testing.T, directory string) {
				t.Helper()
				record := readRecord(t, filepath.Join(directory, "web.json"))
				record.Reference = "ghcr.io/omnara-ai/omnara-api@" + digestFor(1)
				replaceRecord(t, filepath.Join(directory, "web.json"), record)
			},
			want: "reference must start",
		},
		{
			name: "unknown field",
			prepare: func(t *testing.T, directory string) {
				t.Helper()
				path := filepath.Join(directory, "web.json")
				data, err := os.ReadFile(path)
				if err != nil {
					t.Fatalf("read record: %v", err)
				}
				data = []byte(strings.Replace(string(data), "{", "{\"extra\":true,", 1))
				if err := os.WriteFile(path, data, 0o600); err != nil {
					t.Fatalf("write record: %v", err)
				}
			},
			want: "unknown field",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := t.TempDir()
			writeValidRecords(t, directory, "v1.2.3", strings.Repeat("a", 40))
			test.prepare(t, directory)
			output := t.TempDir()
			_, err := generate(
				directory,
				"v1.2.3",
				filepath.Join(output, manifestFilename),
				filepath.Join(output, notesFilename),
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("generate error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNewDigestRecordRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	valid := []string{
		"web",
		"ghcr.io/omnara-ai/omnara-web",
		digestFor(0),
		"v1.2.3",
		strings.Repeat("a", 40),
	}
	tests := []struct {
		name  string
		index int
		value string
	}{
		{name: "unknown name", index: 0, value: "other"},
		{name: "wrong repository", index: 1, value: "ghcr.io/omnara-ai/omnara-api"},
		{name: "invalid digest", index: 2, value: "sha256:abc"},
		{name: "invalid version", index: 3, value: "v01.2.3"},
		{name: "invalid commit", index: 4, value: "abc"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			values := append([]string(nil), valid...)
			values[test.index] = test.value
			if _, err := newDigestRecord(
				values[0],
				values[1],
				values[2],
				values[3],
				values[4],
			); err == nil {
				t.Fatal("newDigestRecord succeeded")
			}
		})
	}
}

func TestVerifyPublic(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	version := "v1.2.3"
	commit := strings.Repeat("a", 40)
	records := writeValidRecords(t, directory, version, commit)
	output := t.TempDir()
	manifestPath := filepath.Join(output, manifestFilename)
	if _, err := generate(
		directory,
		version,
		manifestPath,
		filepath.Join(output, notesFilename),
	); err != nil {
		t.Fatalf("generate: %v", err)
	}

	var inspected []string
	inspect := func(_ context.Context, reference string) (string, error) {
		inspected = append(inspected, reference)
		return "", nil
	}
	if err := verifyPublic(context.Background(), manifestPath, inspect); err != nil {
		t.Fatalf("verify public: %v", err)
	}
	for index, definition := range images {
		if inspected[index] != records[definition.Name].Reference {
			t.Fatalf("inspected[%d] = %q, want %q", index, inspected[index], records[definition.Name].Reference)
		}
	}

	failedReference := records["worker"].Reference
	err := verifyPublic(
		context.Background(),
		manifestPath,
		func(_ context.Context, reference string) (string, error) {
			if reference == failedReference {
				return "unauthorized", errors.New("exit status 1")
			}
			return "", nil
		},
	)
	if err == nil || !strings.Contains(err.Error(), failedReference) ||
		!strings.Contains(err.Error(), "unauthorized") {
		t.Fatalf("verify public error = %v", err)
	}
}

func writeValidRecords(
	t *testing.T,
	directory string,
	version string,
	commit string,
) map[string]digestRecord {
	t.Helper()

	records := make(map[string]digestRecord, len(images))
	for index, definition := range images {
		record, err := newDigestRecord(
			definition.Name,
			definition.Repository,
			digestFor(index),
			version,
			commit,
		)
		if err != nil {
			t.Fatalf("create %s record: %v", definition.Name, err)
		}
		if err := writeDigestRecord(directory, record); err != nil {
			t.Fatalf("write %s record: %v", definition.Name, err)
		}
		records[definition.Name] = record
	}
	return records
}

func digestFor(index int) string {
	return "sha256:" + strings.Repeat(string(rune('a'+index)), 64)
}

func readRecord(t *testing.T, path string) digestRecord {
	t.Helper()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read record: %v", err)
	}
	var record digestRecord
	if err := json.Unmarshal(data, &record); err != nil {
		t.Fatalf("decode record: %v", err)
	}
	return record
}

func replaceRecord(t *testing.T, path string, record digestRecord) {
	t.Helper()

	data, err := encodeJSON(record)
	if err != nil {
		t.Fatalf("encode record: %v", err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write record: %v", err)
	}
}
