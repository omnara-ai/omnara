package omnarad

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

func TestInstallDaemonFreshAndReuse(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	source := filepath.Join(t.TempDir(), "omnarad")
	writeTestExecutable(t, source, "#!/bin/sh\nprintf '1.2.3\\n'\n")
	manifestURL := "https://releases.omnara.test/omnarad/latest/linux-amd64.txt"

	if _, err := installDaemon(context.Background(), home, source, ""); err == nil ||
		err.Error() != "--release-manifest-url is required for a new installation" {
		t.Fatalf("missing manifest URL error = %v", err)
	}

	result, err := installDaemon(context.Background(), home, source, manifestURL)
	if err != nil {
		t.Fatalf("fresh install: %v", err)
	}
	if !result.replaced || result.path != canonicalDaemonPath(home) {
		t.Fatalf("fresh result = %+v", result)
	}
	canonicalBefore, err := os.ReadFile(result.path)
	if err != nil {
		t.Fatalf("read canonical omnarad: %v", err)
	}
	receipt, err := loadInstallReceipt(home)
	if err != nil {
		t.Fatalf("load install receipt: %v", err)
	}
	if receipt.ReleaseManifestURL != manifestURL {
		t.Fatalf("release manifest URL = %q", receipt.ReleaseManifestURL)
	}
	assertInstallFileMode(t, result.path, 0o700)
	assertInstallFileMode(t, filepath.Join(home, installReceiptFileName), 0o600)
	assertInstallFileMode(t, filepath.Join(home, installLockFileName), 0o600)

	newSource := filepath.Join(t.TempDir(), "omnarad")
	writeTestExecutable(t, newSource, "#!/bin/sh\nprintf '2.0.0\\n'\n")
	result, err = installDaemon(context.Background(), home, newSource, manifestURL)
	if err != nil {
		t.Fatalf("reuse install: %v", err)
	}
	if result.replaced {
		t.Fatalf("reuse result = %+v", result)
	}
	canonicalAfter, err := os.ReadFile(result.path)
	if err != nil {
		t.Fatalf("read reused canonical omnarad: %v", err)
	}
	if string(canonicalAfter) != string(canonicalBefore) {
		t.Fatal("reuse replaced the canonical omnarad")
	}
}

func TestValidateReleaseManifestURL(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		url     string
		wantErr bool
	}{
		{
			name: "r2 channel",
			url:  "https://releases.omnara.test/omnarad/latest/linux-amd64.txt",
		},
		{
			name: "github channel",
			url:  "https://github.com/omnara-ai/omnara/releases/download/omnarad-stable/darwin-arm64.txt",
		},
		{
			name:    "unsupported platform",
			url:     "https://releases.omnara.test/omnarad/latest/freebsd-amd64.txt",
			wantErr: true,
		},
		{
			name:    "query",
			url:     "https://releases.omnara.test/omnarad/latest/linux-amd64.txt?channel=stable",
			wantErr: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := validateReleaseManifestURL(test.url)
			if (err != nil) != test.wantErr {
				t.Fatalf("validate release manifest URL error = %v, wantErr = %t", err, test.wantErr)
			}
		})
	}
}

func TestInstallDaemonFreshRetryAfterCopyFailure(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	source := filepath.Join(t.TempDir(), "omnarad")
	writeTestExecutable(t, source, "#!/bin/sh\nprintf '1.2.3\\n'\n")
	if err := os.Chmod(source, 0); err != nil {
		t.Fatalf("make source unreadable: %v", err)
	}
	manifestURL := "https://releases.omnara.test/omnarad/latest/linux-amd64.txt"

	if _, err := installDaemon(context.Background(), home, source, manifestURL); err == nil {
		t.Fatal("fresh install with unreadable source succeeded")
	}
	for _, path := range []string{filepath.Join(home, installReceiptFileName), filepath.Join(home, "bin")} {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("partial fresh install left %s: %v", path, err)
		}
	}
	if err := os.Chmod(source, 0o700); err != nil {
		t.Fatalf("restore source permissions: %v", err)
	}
	if _, err := installDaemon(context.Background(), home, source, manifestURL); err != nil {
		t.Fatalf("retry fresh install: %v", err)
	}
}

func TestInstallDaemonRejectsUnknownState(t *testing.T) {
	manifestURL := "https://releases.omnara.test/omnarad/latest/linux-amd64.txt"
	source := filepath.Join(t.TempDir(), "omnarad")
	writeTestExecutable(t, source, "#!/bin/sh\nprintf '1.2.3\\n'\n")
	tests := []struct {
		name  string
		setup func(string)
		want  string
	}{
		{
			name: "canonical without receipt",
			setup: func(home string) {
				path := canonicalDaemonPath(home)
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				writeTestExecutable(t, path, "#!/bin/sh\nprintf '9.9.9\\n'\n")
			},
			want: "unrecognized installation state",
		},
		{
			name: "daemon state without receipt",
			setup: func(home string) {
				if err := os.MkdirAll(filepath.Join(home, localstore.InstallationsDirName, "existing"), 0o700); err != nil {
					t.Fatal(err)
				}
			},
			want: "unrecognized installation state",
		},
		{
			name: "damaged receipt",
			setup: func(home string) {
				if err := os.MkdirAll(home, 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(home, installReceiptFileName), []byte("{\n"), 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "inspect install receipt",
		},
		{
			name: "future receipt",
			setup: func(home string) {
				if err := localstore.WriteJSONAtomic(filepath.Join(home, installReceiptFileName), installReceipt{
					SchemaVersion:      2,
					InstallMethod:      installReceiptMethod,
					ReleaseManifestURL: manifestURL,
				}, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "not an Omnara-managed install receipt",
		},
		{
			name: "foreign install method",
			setup: func(home string) {
				if err := localstore.WriteJSONAtomic(filepath.Join(home, installReceiptFileName), installReceipt{
					SchemaVersion:      installReceiptVersion,
					InstallMethod:      "package-manager",
					ReleaseManifestURL: manifestURL,
				}, 0o600); err != nil {
					t.Fatal(err)
				}
			},
			want: "not an Omnara-managed install receipt",
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			testCase.setup(home)
			_, err := installDaemon(context.Background(), home, source, manifestURL)
			if err == nil || !strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("install error = %v, want %q", err, testCase.want)
			}
		})
	}
}

func TestInstallDaemonRepairUsesReceipt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	source := filepath.Join(t.TempDir(), "omnarad")
	writeTestExecutable(t, source, "#!/bin/sh\nprintf '1.2.3\\n'\n")
	repairArtifact := []byte("#!/bin/sh\nprintf '2.0.0\\n'\n")
	digest := sha256.Sum256(repairArtifact)
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/latest/linux-amd64.txt":
			_, _ = fmt.Fprintf(w, "version=2.0.0\nurl=%s/omnarad\nsha256=%x\n", server.URL, digest)
		case "/omnarad":
			_, _ = w.Write(repairArtifact)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	manifestURL := server.URL + "/latest/linux-amd64.txt"
	if _, err := installDaemon(context.Background(), home, source, manifestURL); err != nil {
		t.Fatalf("fresh install: %v", err)
	}
	receiptBefore, err := os.ReadFile(filepath.Join(home, installReceiptFileName))
	if err != nil {
		t.Fatalf("read install receipt: %v", err)
	}

	otherURL := "https://other.example.test/omnarad/latest/linux-amd64.txt"
	result, err := installDaemon(context.Background(), home, source, otherURL)
	if err != nil || result.replaced {
		t.Fatalf("healthy managed reuse = result %+v error %v", result, err)
	}
	canonical := canonicalDaemonPath(home)
	if err := os.Remove(canonical); err != nil {
		t.Fatalf("remove canonical omnarad: %v", err)
	}
	result, err = installDaemon(context.Background(), home, source, otherURL)
	if err != nil || !result.replaced {
		t.Fatalf("repair install = result %+v error %v", result, err)
	}
	repaired, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read repaired canonical omnarad: %v", err)
	}
	if string(repaired) != string(repairArtifact) {
		t.Fatalf("repaired canonical omnarad = %q", repaired)
	}
	receiptAfter, err := os.ReadFile(filepath.Join(home, installReceiptFileName))
	if err != nil {
		t.Fatalf("read repaired receipt: %v", err)
	}
	if string(receiptAfter) != string(receiptBefore) {
		t.Fatal("repair changed install receipt")
	}
	if _, err := os.Stat(filepath.Join(home, "bin", installRepairStagingDirName)); !os.IsNotExist(err) {
		t.Fatalf("repair staging directory remains: %v", err)
	}

	if err := os.Remove(canonical); err != nil {
		t.Fatalf("remove repaired canonical omnarad: %v", err)
	}
	if err := os.Mkdir(canonical, 0o700); err != nil {
		t.Fatalf("create unsafe canonical path: %v", err)
	}
	if _, err := installDaemon(context.Background(), home, source, manifestURL); err == nil ||
		!strings.Contains(err.Error(), "must be a regular file") {
		t.Fatalf("unsafe canonical error = %v", err)
	}
}

func TestRunInstallNoStartFreshAndReuseReceipt(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
	defer server.Close()
	setDaemonEnvironment(t, home, server.URL, "token-a")
	manifestURL := "https://releases.omnara.test/omnarad/latest/linux-amd64.txt"
	var stdout strings.Builder
	var stderr strings.Builder
	code := Run(
		context.Background(),
		[]string{"install", "--release-manifest-url", manifestURL, "--no-start"},
		nil,
		&stdout,
		&stderr,
		discardLogger(),
	)
	if code != 0 {
		t.Fatalf("install exit code = %d, stderr = %q", code, stderr.String())
	}
	want := "omnarad installed at " + canonicalDaemonPath(home) + "\n"
	if stdout.String() != want || stderr.Len() != 0 {
		t.Fatalf("install stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := loadInstallReceipt(home); err != nil {
		t.Fatalf("load command install receipt: %v", err)
	}
	if _, err := loadDaemonConfig(home); err != nil {
		t.Fatalf("load command daemon config: %v", err)
	}
	writeTestExecutable(t, canonicalDaemonPath(home), "#!/bin/sh\nprintf '1.2.3\\n'\n")

	stdout.Reset()
	stderr.Reset()
	code = Run(
		context.Background(),
		[]string{"install", "--no-start"},
		nil,
		&stdout,
		&stderr,
		discardLogger(),
	)
	if code != 0 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("repeat install exit code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunInstallHoldsInstallLockThroughConfiguration(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	observed := make(chan error, 1)
	server := bootstrapServer(t, "token-a", "inst-a", "mch-a", func() {
		lock, acquired, err := tryAcquireInstallLock(home)
		if lock != nil {
			_ = lock.Release()
		}
		if err != nil {
			observed <- err
			return
		}
		if acquired {
			observed <- errors.New("install lock was released before configuration completed")
			return
		}
		observed <- nil
	})
	defer server.Close()
	setDaemonEnvironment(t, home, server.URL, "token-a")
	var stderr strings.Builder
	if code := Run(
		context.Background(),
		[]string{
			"install",
			"--release-manifest-url",
			"https://releases.omnara.test/omnarad/latest/linux-amd64.txt",
			"--no-start",
		},
		nil,
		&strings.Builder{},
		&stderr,
		discardLogger(),
	); code != 0 {
		t.Fatalf("install exit code = %d, stderr = %q", code, stderr.String())
	}
	if err := <-observed; err != nil {
		t.Fatal(err)
	}
}

func TestRunInstallRestartsCanonicalDaemon(t *testing.T) {
	if os.Getenv("OMNARA_INSTALL_RESTART_TEST") == "1" {
		os.Exit(Run(
			context.Background(),
			[]string{"install"},
			os.Stdin,
			os.Stdout,
			os.Stderr,
			discardLogger(),
		))
	}

	home := t.TempDir()
	server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
	defer server.Close()
	setDaemonEnvironment(t, home, server.URL, "token-a")
	if err := localstore.WriteJSONAtomic(filepath.Join(home, installReceiptFileName), installReceipt{
		SchemaVersion:      installReceiptVersion,
		InstallMethod:      installReceiptMethod,
		ReleaseManifestURL: "https://releases.omnara.test/omnarad/latest/linux-amd64.txt",
	}, 0o600); err != nil {
		t.Fatalf("write install receipt: %v", err)
	}
	canonical := canonicalDaemonPath(home)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatalf("create daemon bin directory: %v", err)
	}
	outputPath := filepath.Join(t.TempDir(), "restart-args")
	writeTestExecutable(t, canonical, `#!/bin/sh
if [ "$1" = "--version" ]; then
  printf '1.2.3\n'
  exit 0
fi
printf '%s\n' "$@" > "$OMNARA_INSTALL_RESTART_OUTPUT"
`)
	cmd := exec.Command(os.Args[0], "-test.run=^TestRunInstallRestartsCanonicalDaemon$")
	cmd.Env = append(
		os.Environ(),
		"OMNARA_INSTALL_RESTART_TEST=1",
		"OMNARA_INSTALL_RESTART_OUTPUT="+outputPath,
	)
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("run install helper: %v: %s", err, output)
	}
	if got := readTestFile(t, outputPath); got != restartSubcommand+"\n" {
		t.Fatalf("install reexec args = %q", got)
	}
}

func assertInstallFileMode(t *testing.T, path string, want os.FileMode) {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	if info.Mode().Perm() != want {
		t.Fatalf("%s mode = %o, want %o", path, info.Mode().Perm(), want)
	}
}
