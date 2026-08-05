package omnarad

import (
	"context"
	"crypto/sha256"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/omnara-ai/omnara/internal/daemonversion"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

func TestParseReleaseManifest(t *testing.T) {
	t.Parallel()
	validChecksum := strings.Repeat("a", 64)
	manifest, err := parseReleaseManifest([]byte(
		"version=1.2.3\nurl=https://releases.omnara.test/omnarad\nsha256=" +
			strings.ToUpper(validChecksum) + "\nunknown=ignored\n",
	))
	if err != nil {
		t.Fatalf("parse release manifest: %v", err)
	}
	if manifest.Version != "1.2.3" || manifest.URL != "https://releases.omnara.test/omnarad" ||
		manifest.SHA256 != validChecksum {
		t.Fatalf("release manifest = %+v", manifest)
	}
	for _, body := range []string{
		"url=https://releases.omnara.test/omnarad\nsha256=" + validChecksum,
		"version=1.2.3\nversion=1.2.4\nurl=https://releases.omnara.test/omnarad\nsha256=" + validChecksum,
		"version=01.2.3\nurl=https://releases.omnara.test/omnarad\nsha256=" + validChecksum,
		"version=1.2.3\nurl=http://releases.omnara.test/omnarad\nsha256=" + validChecksum,
		"version=1.2.3\nurl=https://:443/omnarad\nsha256=" + validChecksum,
		"version=1.2.3\nurl=https://releases.omnara.test/omnarad\nsha256=invalid",
	} {
		if _, err := parseReleaseManifest([]byte(body)); err == nil {
			t.Fatalf("invalid release manifest accepted: %q", body)
		}
	}
}

func TestDaemonUpdateDiscoveryRequiresManagedInstall(t *testing.T) {
	var requests atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		requests.Add(1)
	}))
	t.Cleanup(server.Close)

	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	canonical := canonicalDaemonPath(home)
	writeTestExecutable(t, canonical, "#!/bin/sh\nprintf '1.2.3\\n'\n")
	assertDiscovery := func(name, executable, currentVersion string, wantError bool) {
		t.Helper()
		before := requests.Load()
		status, _, err := discoverDaemonUpdate(context.Background(), home, executable, currentVersion)
		if (err != nil) != wantError || status != updateSkipped {
			t.Fatalf("%s: discovery = status %d error %v", name, status, err)
		}
		if requests.Load() != before {
			t.Fatalf("%s: update discovery requested the release manifest", name)
		}
	}

	assertDiscovery("missing receipt", canonical, "1.2.3", false)
	assertDiscovery("missing receipt with custom version", canonical, "custom-build", false)
	receiptPath := filepath.Join(home, installReceiptFileName)
	if err := os.WriteFile(receiptPath, []byte("{}\n"), 0o600); err != nil {
		t.Fatalf("write invalid receipt: %v", err)
	}
	assertDiscovery("invalid receipt", canonical, "1.2.3", true)
	if err := localstore.WriteJSONAtomic(receiptPath, installReceipt{
		SchemaVersion:      installReceiptVersion,
		InstallMethod:      installReceiptMethod,
		ReleaseManifestURL: server.URL,
	}, 0o600); err != nil {
		t.Fatalf("write install receipt: %v", err)
	}
	assertDiscovery("non-concrete receipt", canonical, "1.2.3", true)
	if err := localstore.WriteJSONAtomic(receiptPath, installReceipt{
		SchemaVersion:      installReceiptVersion,
		InstallMethod:      installReceiptMethod,
		ReleaseManifestURL: server.URL + "/latest/linux-amd64.txt?channel=stable",
	}, 0o600); err != nil {
		t.Fatalf("write install receipt: %v", err)
	}
	assertDiscovery("receipt with query", canonical, "1.2.3", true)
	if err := localstore.WriteJSONAtomic(receiptPath, installReceipt{
		SchemaVersion:      installReceiptVersion,
		InstallMethod:      installReceiptMethod,
		ReleaseManifestURL: server.URL + "/latest/linux-amd64.txt",
	}, 0o600); err != nil {
		t.Fatalf("write install receipt: %v", err)
	}
	otherExecutable := filepath.Join(home, "other-omnarad")
	writeTestExecutable(t, otherExecutable, "#!/bin/sh\nprintf '1.2.3\\n'\n")
	assertDiscovery("noncanonical executable", otherExecutable, "1.2.3", false)
	if err := os.Remove(canonical); err != nil {
		t.Fatalf("remove canonical executable: %v", err)
	}
	assertDiscovery("missing canonical executable", canonical, "1.2.3", true)
	writeTestExecutable(t, canonical, "#!/bin/sh\nprintf '1.2.3\\n'\n")
	assertDiscovery("development build", canonical, daemonversion.Development, false)
}

func TestDaemonUpdateDiscoveryAndStaging(t *testing.T) {
	home := t.TempDir()
	binDir := filepath.Join(home, "bin")
	if err := os.Mkdir(binDir, 0o700); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	canonical := canonicalDaemonPath(home)
	writeTestExecutable(t, canonical, "#!/bin/sh\nprintf '1.2.3\\n'\n")
	t.Setenv("OMNARA_MACHINE_TOKEN", "environment-secret")
	t.Setenv("OMNARA_DAEMON_SEED_PATH", "/untrusted/seed")

	versionEnvPath := filepath.Join(t.TempDir(), "version-env")
	artifact := fmt.Sprintf("#!/bin/sh\nenv > %q\nprintf '1.2.4\\n'\n", versionEnvPath)
	artifactChecksum := fmt.Sprintf("%x", sha256.Sum256([]byte(artifact)))
	manifestVersion := "1.2.3"
	var manifestRequests atomic.Int32
	var artifactRequests atomic.Int32
	var server *httptest.Server
	server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			t.Errorf("release request included authorization header")
		}
		switch r.URL.Path {
		case "/omnarad/latest/linux-amd64.txt":
			manifestRequests.Add(1)
			_, _ = fmt.Fprintf(
				w,
				"version=%s\nurl=%s/artifact\nsha256=%s\n",
				manifestVersion,
				server.URL,
				artifactChecksum,
			)
		case "/artifact":
			artifactRequests.Add(1)
			_, _ = w.Write([]byte(artifact))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	if err := localstore.WriteJSONAtomic(filepath.Join(home, installReceiptFileName), installReceipt{
		SchemaVersion:      installReceiptVersion,
		InstallMethod:      installReceiptMethod,
		ReleaseManifestURL: server.URL + "/omnarad/latest/linux-amd64.txt",
	}, 0o600); err != nil {
		t.Fatalf("write install receipt: %v", err)
	}

	status, _, err := discoverDaemonUpdate(context.Background(), home, canonical, "1.2.3")
	if err != nil || status != updateUnchanged {
		t.Fatalf("unchanged discovery = status %d error %v", status, err)
	}
	if artifactRequests.Load() != 0 {
		t.Fatalf("unchanged discovery downloaded artifact %d times", artifactRequests.Load())
	}

	manifestVersion = "1.2.4"
	status, update, err := discoverDaemonUpdate(context.Background(), home, canonical, "1.2.3")
	if err != nil || status != updateAvailable {
		t.Fatalf("available discovery = status %d error %v", status, err)
	}
	if manifestRequests.Load() != 2 || artifactRequests.Load() != 0 {
		t.Fatalf("release requests = manifest:%d artifact:%d", manifestRequests.Load(), artifactRequests.Load())
	}
	stalePath := filepath.Join(binDir, updateStagingDirName, "stale")
	if err := os.MkdirAll(filepath.Dir(stalePath), 0o700); err != nil {
		t.Fatalf("create stale update stage: %v", err)
	}
	if err := os.WriteFile(stalePath, []byte("stale"), 0o600); err != nil {
		t.Fatalf("write stale update stage: %v", err)
	}
	staged, err := stageDaemonRelease(context.Background(), home, update, updateStagingDirName)
	if err != nil {
		t.Fatalf("stage daemon update: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(staged.dir) })
	if _, err := os.Stat(stalePath); !os.IsNotExist(err) {
		t.Fatalf("stale update stage still exists: %v", err)
	}
	if artifactRequests.Load() != 1 {
		t.Fatalf("artifact requests = %d, want 1", artifactRequests.Load())
	}
	versionEnv, err := os.ReadFile(versionEnvPath)
	if err != nil {
		t.Fatalf("read staged version environment: %v", err)
	}
	for _, key := range []string{
		"OMNARA_MACHINE_TOKEN=",
		"OMNARA_DAEMON_SEED_PATH=",
		"OMNARA_INSTALLATION_ID=",
		"OMNARA_MACHINE_ID=",
	} {
		if strings.Contains(string(versionEnv), key) {
			t.Fatalf("staged version environment contains %q: %s", key, versionEnv)
		}
	}
	valid, err := revalidateDaemonUpdate(context.Background(), home, canonical, "1.2.3", staged)
	if err != nil || !valid {
		t.Fatalf("revalidate staged update = valid %t error %v", valid, err)
	}
	writeTestExecutable(t, canonical, "#!/bin/sh\nprintf '1.2.5\\n'\n")
	valid, err = revalidateDaemonUpdate(context.Background(), home, canonical, "1.2.3", staged)
	if err != nil || valid {
		t.Fatalf("revalidate after canonical replacement = valid %t error %v", valid, err)
	}
}

func TestDownloadPublicReleaseRejectsInsecureRedirect(t *testing.T) {
	var redirectedRequests atomic.Int32
	redirected := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		redirectedRequests.Add(1)
	}))
	t.Cleanup(redirected.Close)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, redirected.URL, http.StatusFound)
	}))
	t.Cleanup(server.Close)
	if err := downloadPublicRelease(context.Background(), server.URL, 1024, io.Discard); err == nil {
		t.Fatal("insecure release redirect succeeded")
	}
	if redirectedRequests.Load() != 0 {
		t.Fatalf("redirect target requests = %d, want 0", redirectedRequests.Load())
	}
}

func TestStageDaemonUpdateVerifiesChecksumBeforeExecution(t *testing.T) {
	home := t.TempDir()
	if err := os.Mkdir(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatalf("create bin dir: %v", err)
	}
	marker := filepath.Join(home, "executed")
	artifact := "#!/bin/sh\ntouch \"" + marker + "\"\nprintf '1.2.4\\n'\n"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(artifact))
	}))
	t.Cleanup(server.Close)
	_, err := stageDaemonRelease(context.Background(), home, daemonUpdate{manifest: releaseManifest{
		Version: "1.2.4",
		URL:     server.URL,
		SHA256:  strings.Repeat("0", 64),
	}}, updateStagingDirName)
	if err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("stage daemon update error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("unverified artifact executed: %v", err)
	}
}

func TestReexecDaemonUsesCanonicalCommand(t *testing.T) {
	if os.Getenv("OMNARA_REEXEC_TEST") == "1" {
		if err := reexecDaemon(
			context.Background(),
			os.Getenv("OMNARA_REEXEC_BINARY"),
			strings.Fields(os.Getenv("OMNARA_REEXEC_ARGS"))...,
		); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		os.Exit(2)
	}

	for _, args := range [][]string{{"run-service"}, {"run-service", supervisedServiceFlag}, {"restart"}} {
		t.Run(strings.Join(args, "_"), func(t *testing.T) {
			dir := t.TempDir()
			outputPath := filepath.Join(dir, "argv")
			binaryPath := filepath.Join(dir, "omnarad")
			writeTestExecutable(t, binaryPath, "#!/bin/sh\nprintf '%s\\n' \"$0\" \"$@\" > \"$OMNARA_REEXEC_OUTPUT\"\n")
			cmd := exec.Command(os.Args[0], "-test.run=^TestReexecDaemonUsesCanonicalCommand$")
			cmd.Env = append(
				os.Environ(),
				"OMNARA_REEXEC_TEST=1",
				"OMNARA_REEXEC_BINARY="+binaryPath,
				"OMNARA_REEXEC_ARGS="+strings.Join(args, " "),
				"OMNARA_REEXEC_OUTPUT="+outputPath,
			)
			if output, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("reexec helper: %v: %s", err, output)
			}
			output, err := os.ReadFile(outputPath)
			if err != nil {
				t.Fatalf("read reexec argv: %v", err)
			}
			want := binaryPath + "\n" + strings.Join(args, "\n") + "\n"
			if string(output) != want {
				t.Fatalf("reexec argv = %q, want %q", output, want)
			}
		})
	}
}

func TestReexecDaemonSkipsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := reexecDaemon(ctx, filepath.Join(t.TempDir(), "missing-omnarad"), "run-service"); err != nil {
		t.Fatalf("reexec canceled daemon: %v", err)
	}
}
