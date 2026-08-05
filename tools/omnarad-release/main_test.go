package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

const testReleaseDownloads = "https://github.example/test-owner/test-repo/releases/download"

func TestResolveVersion(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		event        string
		refName      string
		ref          string
		inputVersion string
		want         string
		wantErr      string
	}{
		{
			name:    "tag",
			event:   "push",
			refName: "omnarad-v1.2.3",
			want:    "1.2.3",
		},
		{
			name:         "manual",
			event:        "workflow_dispatch",
			ref:          "refs/heads/main",
			inputVersion: "1.2.3",
			want:         "1.2.3",
		},
		{
			name:    "wrong tag prefix",
			event:   "push",
			refName: "v1.2.3",
			wantErr: "release tag",
		},
		{
			name:    "noncanonical tag",
			event:   "push",
			refName: "omnarad-v01.2.3",
			wantErr: "version",
		},
		{
			name:    "overflowing tag",
			event:   "push",
			refName: "omnarad-v18446744073709551616.0.0",
			wantErr: "version",
		},
		{
			name:         "manual wrong branch",
			event:        "workflow_dispatch",
			ref:          "refs/heads/feature",
			inputVersion: "1.2.3",
			wantErr:      "must run from main",
		},
		{
			name:    "unsupported event",
			event:   "pull_request",
			wantErr: "unsupported release event",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := resolveVersion(test.event, test.refName, test.ref, test.inputVersion)
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

func TestReleaseDownloadBaseURL(t *testing.T) {
	for _, test := range []struct {
		name       string
		serverURL  string
		repository string
		want       string
		wantErr    string
	}{
		{
			name:       "github",
			serverURL:  "https://github.com",
			repository: "test-owner/test-repo",
			want:       "https://github.com/test-owner/test-repo/releases/download",
		},
		{
			name:       "trailing slash",
			serverURL:  "https://github.example/",
			repository: "test-owner/test-repo",
			want:       "https://github.example/test-owner/test-repo/releases/download",
		},
		{
			name:       "insecure server",
			serverURL:  "http://github.example",
			repository: "test-owner/test-repo",
			wantErr:    "HTTPS server URL",
		},
		{
			name:       "invalid repository",
			serverURL:  "https://github.example",
			repository: "test-owner",
			wantErr:    "OWNER/REPOSITORY",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Setenv("GITHUB_SERVER_URL", test.serverURL)
			t.Setenv("GITHUB_REPOSITORY", test.repository)

			got, err := releaseDownloadBaseURL()
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("release download base URL error = %v, want %q", err, test.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("release download base URL: %v", err)
			}
			if got != test.want {
				t.Fatalf("release download base URL = %q, want %q", got, test.want)
			}
		})
	}
}

func TestGenerateDeterministicChecksums(t *testing.T) {
	t.Parallel()

	const version = "1.2.3"
	directory := newArtifactDirectory(t)
	if err := generate(directory, version, testReleaseDownloads); err != nil {
		t.Fatalf("generate checksums: %v", err)
	}
	first := readFile(t, filepath.Join(directory, checksumsFilename))

	if err := generate(directory, version, testReleaseDownloads); err != nil {
		t.Fatalf("regenerate checksums: %v", err)
	}
	if actual := readFile(t, filepath.Join(directory, checksumsFilename)); !bytes.Equal(actual, first) {
		t.Fatal("checksum generation is not deterministic")
	}

	var expected strings.Builder
	for _, name := range artifactFilenames {
		hash := sha256.Sum256([]byte("artifact " + name))
		expected.WriteString(hex.EncodeToString(hash[:]))
		expected.WriteString("  ")
		expected.WriteString(name)
		expected.WriteByte('\n')
	}
	if string(first) != expected.String() {
		t.Fatalf("SHA256SUMS = %q, want %q", first, expected.String())
	}
	for _, artifact := range artifactFilenames {
		hash := sha256.Sum256([]byte("artifact " + artifact))
		want := []byte(
			"version=1.2.3\n" +
				"url=" + testReleaseDownloads + "/omnarad-v1.2.3/" + artifact + "\n" +
				"sha256=" + hex.EncodeToString(hash[:]) + "\n",
		)
		if got := readFile(t, filepath.Join(directory, manifestFilename(artifact))); !bytes.Equal(got, want) {
			t.Fatalf("%s = %q, want %q", manifestFilename(artifact), got, want)
		}
	}
}

func TestGenerateRejectsInvalidArtifactSets(t *testing.T) {
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
				if err := os.Remove(filepath.Join(directory, artifactFilenames[0])); err != nil {
					t.Fatalf("remove artifact: %v", err)
				}
			},
			want: "missing release artifact",
		},
		{
			name: "unexpected",
			prepare: func(t *testing.T, directory string) {
				t.Helper()
				writeFile(t, filepath.Join(directory, "omnarad-freebsd-amd64"), []byte("unexpected"))
			},
			want: "unexpected release artifact",
		},
		{
			name: "symlinked",
			prepare: func(t *testing.T, directory string) {
				t.Helper()
				path := filepath.Join(directory, artifactFilenames[0])
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove artifact: %v", err)
				}
				if err := os.Symlink(filepath.Join(directory, artifactFilenames[1]), path); err != nil {
					t.Fatalf("symlink artifact: %v", err)
				}
			},
			want: "must be a regular file",
		},
		{
			name: "nonregular",
			prepare: func(t *testing.T, directory string) {
				t.Helper()
				path := filepath.Join(directory, artifactFilenames[0])
				if err := os.Remove(path); err != nil {
					t.Fatalf("remove artifact: %v", err)
				}
				if err := os.Mkdir(path, 0o755); err != nil {
					t.Fatalf("create artifact directory: %v", err)
				}
			},
			want: "must be a regular file",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := newArtifactDirectory(t)
			test.prepare(t, directory)
			if err := generate(directory, "1.2.3", testReleaseDownloads); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("generate error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestVerifyRejectsMutatedBundle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*testing.T, string)
		want   string
	}{
		{
			name: "artifact",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				appendFile(t, filepath.Join(directory, artifactFilenames[0]), []byte("mutated"))
			},
			want: checksumsFilename,
		},
		{
			name: "checksums",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				appendFile(t, filepath.Join(directory, checksumsFilename), []byte("mutated"))
			},
			want: checksumsFilename,
		},
		{
			name: "manifest",
			mutate: func(t *testing.T, directory string) {
				t.Helper()
				appendFile(t, filepath.Join(directory, manifestFilename(artifactFilenames[0])), []byte("mutated"))
			},
			want: manifestFilename(artifactFilenames[0]),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := newArtifactDirectory(t)
			if err := generate(directory, "1.2.3", testReleaseDownloads); err != nil {
				t.Fatalf("generate checksums: %v", err)
			}
			test.mutate(t, directory)
			if err := verify(directory, "1.2.3", testReleaseDownloads); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("verify error = %v", err)
			}
		})
	}
}

func TestVerifyRejectsSymlinkedChecksums(t *testing.T) {
	t.Parallel()

	directory := newArtifactDirectory(t)
	target := filepath.Join(directory, artifactFilenames[0])
	link := filepath.Join(directory, checksumsFilename)
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("symlink checksums: %v", err)
	}
	if err := verify(directory, "1.2.3", testReleaseDownloads); err == nil ||
		!strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("verify error = %v", err)
	}
}

func TestRejectsSymlinkedReleaseDirectory(t *testing.T) {
	t.Parallel()

	directory := newArtifactDirectory(t)
	link := filepath.Join(t.TempDir(), "release")
	if err := os.Symlink(directory, link); err != nil {
		t.Fatalf("symlink release directory: %v", err)
	}
	if err := generate(link, "1.2.3", testReleaseDownloads); err == nil ||
		!strings.Contains(err.Error(), "must be a real directory") {
		t.Fatalf("generate error = %v", err)
	}
}

func TestCheckPromotion(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		releaseTags string
		version     string
		wantErr     string
	}{
		{
			name:        "newest published release",
			releaseTags: "cluster-v1.0.0\nomnarad-v1.2.3\nomnarad-v1.2.2\n",
			version:     "1.2.3",
		},
		{
			name:        "newer release published",
			releaseTags: "omnarad-v1.2.4\nomnarad-v1.2.3\n",
			version:     "1.2.3",
			wantErr:     "newer release 1.2.4",
		},
		{
			name:        "target release missing",
			releaseTags: "omnarad-v1.2.2\n",
			version:     "1.2.3",
			wantErr:     "was not found",
		},
		{
			name:        "invalid published release",
			releaseTags: "omnarad-vinvalid\nomnarad-v1.2.3\n",
			version:     "1.2.3",
			wantErr:     "is invalid",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := checkPromotion(test.releaseTags, test.version)
			if test.wantErr == "" {
				if err != nil {
					t.Fatalf("check promotion: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("check promotion error = %v, want %q", err, test.wantErr)
			}
		})
	}
}

func TestPromote(t *testing.T) {
	for _, test := range []struct {
		name           string
		currentVersion string
		wantCommand    string
		unwanted       string
	}{
		{
			name:        "create channel",
			wantCommand: "release create omnarad-stable",
			unwanted:    "release upload omnarad-stable",
		},
		{
			name:           "update channel",
			currentVersion: "1.2.2",
			wantCommand:    "release upload omnarad-stable",
			unwanted:       "release create omnarad-stable",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			const version = "1.2.3"
			directory := newArtifactDirectory(t)
			if err := generate(directory, version, testReleaseDownloads); err != nil {
				t.Fatalf("generate release bundle: %v", err)
			}
			published := t.TempDir()
			if test.currentVersion != "" {
				current := newArtifactDirectory(t)
				if err := generate(current, test.currentVersion, testReleaseDownloads); err != nil {
					t.Fatalf("generate current release bundle: %v", err)
				}
				for _, artifact := range artifactFilenames {
					name := manifestFilename(artifact)
					writeFile(t, filepath.Join(published, name), readFile(t, filepath.Join(current, name)))
				}
				if err := os.Remove(filepath.Join(published, "linux-amd64.txt")); err != nil {
					t.Fatalf("remove current manifest: %v", err)
				}
			}
			logPath := installFakeGH(t, published, test.currentVersion != "")

			err := promote(
				directory,
				version,
				"omnarad-v"+version,
				strings.Repeat("a", 40),
				testReleaseDownloads,
			)
			if err != nil {
				t.Fatalf("promote release: %v", err)
			}
			logBody := string(readFile(t, logPath))
			if !strings.Contains(logBody, test.wantCommand) {
				t.Fatalf("gh command log = %q, want %q", logBody, test.wantCommand)
			}
			if strings.Contains(logBody, test.unwanted) {
				t.Fatalf("gh command log = %q, do not want %q", logBody, test.unwanted)
			}
			if test.currentVersion != "" && !strings.Contains(logBody, "--clobber") {
				t.Fatalf("gh command log = %q, want --clobber", logBody)
			}
			tagMove := "api --method PATCH repos/{owner}/{repo}/git/refs/tags/omnarad-stable -f sha=" +
				strings.Repeat("a", 40) + " -F force=true"
			if !strings.Contains(logBody, tagMove) {
				t.Fatalf("gh command log = %q, want %q", logBody, tagMove)
			}
			if strings.Index(logBody, tagMove) < strings.LastIndex(logBody, "release download omnarad-stable") {
				t.Fatalf("stable tag moved before published manifests were verified: %q", logBody)
			}
		})
	}
}

func installFakeGH(t *testing.T, published string, releaseExists bool) string {
	t.Helper()

	directory := t.TempDir()
	logPath := filepath.Join(directory, "commands")
	writeExecutable(t, filepath.Join(directory, "gh"), `#!/bin/sh
set -eu
printf '%s\n' "$*" >> "$OMNARAD_TEST_GH_LOG"
case "$1:$2" in
  api:--paginate)
	printf '%s\n' 'cluster-v1.0.0' 'omnarad-v1.2.3'
	if [ "$OMNARAD_TEST_RELEASE_EXISTS" = 1 ]; then
	  printf '%s\n' 'omnarad-stable'
	fi
	;;
  api:--method)
	;;
  release:create|release:upload)
    shift 3
    for argument do
      if [ -f "$argument" ]; then
        cp "$argument" "$OMNARAD_TEST_PUBLISHED/$(basename "$argument")"
      fi
    done
    ;;
  release:download)
    pattern=
    while [ "$#" -gt 0 ]; do
      if [ "$1" = --pattern ]; then
        pattern=$2
        shift 2
      else
        shift
      fi
    done
    test -n "$pattern"
    cat "$OMNARAD_TEST_PUBLISHED/$pattern"
    ;;
  *)
    exit 2
    ;;
esac
`)
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("OMNARAD_TEST_GH_LOG", logPath)
	t.Setenv("OMNARAD_TEST_PUBLISHED", published)
	if releaseExists {
		t.Setenv("OMNARAD_TEST_RELEASE_EXISTS", "1")
	} else {
		t.Setenv("OMNARAD_TEST_RELEASE_EXISTS", "0")
	}
	return logPath
}

func newArtifactDirectory(t *testing.T) string {
	t.Helper()

	directory := t.TempDir()
	for _, name := range artifactFilenames {
		writeFile(t, filepath.Join(directory, name), []byte("artifact "+name))
	}
	return directory
}

func writeFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	if err := os.WriteFile(path, contents, 0o644); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
}

func writeExecutable(t *testing.T, path string, contents string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}

func appendFile(t *testing.T, path string, contents []byte) {
	t.Helper()
	file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	if _, err := file.Write(contents); err != nil {
		_ = file.Close()
		t.Fatalf("append %s: %v", path, err)
	}
	if err := file.Close(); err != nil {
		t.Fatalf("close %s: %v", path, err)
	}
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	contents, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return contents
}
