//go:build darwin || linux

package omnarad

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/user"
	"path/filepath"
	"strings"
	"testing"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/machinedaemon/statedb"
)

func TestRunUninstallRemovesOwnedInstallation(t *testing.T) {
	root := t.TempDir()
	home := filepath.Join(root, "daemon-home")
	userHome := filepath.Join(root, "user-home")
	if err := os.MkdirAll(filepath.Join(userHome, ".local", "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestInstallReceipt(t, home)
	canonical := canonicalDaemonPath(home)
	if err := os.MkdirAll(filepath.Dir(canonical), 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestExecutable(t, canonical, "#!/bin/sh\nexit 0\n")
	link := filepath.Join(userHome, ".local", "bin", "omnarad")
	if err := os.Symlink(canonical, link); err != nil {
		t.Fatal(err)
	}
	profile := filepath.Join(userHome, ".zshrc")
	profileBody := "# managed by omnarad\npath=(\"" + filepath.Dir(canonical) + "\" $path)\n"
	if err := os.WriteFile(profile, []byte(profileBody), 0o600); err != nil {
		t.Fatal(err)
	}
	externalBinary := filepath.Join(root, "external-omnarad")
	writeTestExecutable(t, externalBinary, "#!/bin/sh\nexit 0\n")
	t.Setenv("HOME", userHome)
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("PATH", t.TempDir())

	var stdout strings.Builder
	var stderr strings.Builder
	if code := Run(
		context.Background(),
		[]string{"uninstall", "--yes"},
		nil,
		&stdout,
		&stderr,
		discardLogger(),
	); code != 0 {
		t.Fatalf("uninstall exit code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "omnarad uninstalled from "+home+"\n" || stderr.Len() != 0 {
		t.Fatalf("uninstall stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("daemon home still exists: %v", err)
	}
	if _, err := os.Lstat(link); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("PATH symlink still exists: %v", err)
	}
	if got := readTestFile(t, profile); got != profileBody {
		t.Fatalf("shell profile changed: %q", got)
	}
	if _, err := os.Stat(externalBinary); err != nil {
		t.Fatalf("external binary was removed: %v", err)
	}
	matches, err := filepath.Glob(filepath.Join(root, ".daemon-home.uninstall-*"))
	if err != nil || len(matches) != 0 {
		t.Fatalf("uninstall tombstones = %v, error = %v", matches, err)
	}
}

func TestRunUninstallAcceptsConfigOnlyInstallation(t *testing.T) {
	home := filepath.Join(t.TempDir(), "daemon-home")
	userHome := filepath.Join(t.TempDir(), "user-home")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	reports := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reports++
		if got := r.URL.Query().Get("stage"); got != "daemon_uninstalled" {
			t.Errorf("uninstall stage = %q", got)
		}
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	config := testUninstallConfig()
	config.APIURL = server.URL
	writeTestDaemonConfig(t, home, config)
	t.Setenv("HOME", userHome)
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("PATH", t.TempDir())
	var stderr strings.Builder
	if code := Run(
		context.Background(),
		[]string{"uninstall", "--yes"},
		nil,
		&strings.Builder{},
		&stderr,
		discardLogger(),
	); code != 0 {
		t.Fatalf("uninstall exit code = %d, stderr = %q", code, stderr.String())
	}
	if _, err := os.Lstat(home); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("daemon home still exists: %v", err)
	}
	if reports != 1 {
		t.Fatalf("successful uninstall reports = %d, want 1", reports)
	}
	reportCtx, cancel := context.WithCancel(context.Background())
	cancel()
	reportUninstall(reportCtx, &config, nil, discardLogger())
	if reports != 2 {
		t.Fatalf("canceled-context uninstall reports = %d, want 2", reports)
	}
}

func TestRunUninstallRequiresConfirmation(t *testing.T) {
	home := filepath.Join(t.TempDir(), "daemon-home")
	userHome := filepath.Join(t.TempDir(), "user-home")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	writeTestInstallReceipt(t, home)
	t.Setenv("HOME", userHome)
	t.Setenv("OMNARA_HOME", home)
	var stderr strings.Builder
	if code := Run(
		context.Background(),
		[]string{"uninstall"},
		nil,
		&strings.Builder{},
		&stderr,
		discardLogger(),
	); code != 1 {
		t.Fatalf("uninstall exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "rerun with --yes") {
		t.Fatalf("uninstall stderr = %q", stderr.String())
	}
	if _, err := os.Stat(home); err != nil {
		t.Fatalf("daemon home was removed: %v", err)
	}
}

func TestInspectUninstallHomeRejectsUnsafeTargets(t *testing.T) {
	t.Run("operating-system user home", func(t *testing.T) {
		account, err := user.Current()
		if err != nil {
			t.Fatal(err)
		}
		userHome := t.TempDir()
		t.Setenv("HOME", userHome)
		if _, err := inspectUninstallHome(account.HomeDir); err == nil ||
			!strings.Contains(err.Error(), "user home directory") {
			t.Fatalf("inspect error = %v", err)
		}
	})

	t.Run("current user home", func(t *testing.T) {
		home := t.TempDir()
		writeTestInstallReceipt(t, home)
		t.Setenv("HOME", home)
		if _, err := inspectUninstallHome(home); err == nil ||
			!strings.Contains(err.Error(), "user home directory") {
			t.Fatalf("inspect error = %v", err)
		}
	})

	t.Run("ancestor of current user home", func(t *testing.T) {
		home := filepath.Join(t.TempDir(), "daemon-home")
		userHome := filepath.Join(home, "user-home")
		if err := os.MkdirAll(userHome, 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestInstallReceipt(t, home)
		t.Setenv("HOME", userHome)
		if _, err := inspectUninstallHome(home); err == nil ||
			!strings.Contains(err.Error(), "user home directory") {
			t.Fatalf("inspect error = %v", err)
		}
	})

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		writeTestInstallReceipt(t, target)
		home := filepath.Join(root, "home")
		if err := os.Symlink(target, home); err != nil {
			t.Fatal(err)
		}
		userHome := filepath.Join(root, "user-home")
		if err := os.Mkdir(userHome, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", userHome)
		if _, err := inspectUninstallHome(home); err == nil ||
			!strings.Contains(err.Error(), "not a symlink") {
			t.Fatalf("inspect error = %v", err)
		}
	})

	t.Run("binary only", func(t *testing.T) {
		root := t.TempDir()
		home := filepath.Join(root, "home")
		if err := os.MkdirAll(filepath.Dir(canonicalDaemonPath(home)), 0o700); err != nil {
			t.Fatal(err)
		}
		writeTestExecutable(t, canonicalDaemonPath(home), "#!/bin/sh\nexit 0\n")
		userHome := filepath.Join(root, "user-home")
		if err := os.Mkdir(userHome, 0o700); err != nil {
			t.Fatal(err)
		}
		t.Setenv("HOME", userHome)
		if _, err := inspectUninstallHome(home); err == nil ||
			!strings.Contains(err.Error(), "valid daemon config or install receipt") {
			t.Fatalf("inspect error = %v", err)
		}
	})
}

func TestInspectUninstallHomeRejectsUnexpectedBindings(t *testing.T) {
	for _, testCase := range []struct {
		name string
		path func(string) string
		want string
	}{
		{
			name: "installation",
			path: func(home string) string {
				return filepath.Join(home, localstore.InstallationsDirName, "inst-other")
			},
			want: "configured installation",
		},
		{
			name: "machine",
			path: func(home string) string {
				return filepath.Join(
					home,
					localstore.InstallationsDirName,
					"inst-a",
					localstore.MachinesDirName,
					"mch-other",
				)
			},
			want: "configured machine",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			home := filepath.Join(root, "home")
			userHome := filepath.Join(root, "user-home")
			if err := os.MkdirAll(testCase.path(home), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(userHome, 0o700); err != nil {
				t.Fatal(err)
			}
			writeTestDaemonConfig(t, home, testUninstallConfig())
			t.Setenv("HOME", userHome)
			if _, err := inspectUninstallHome(home); err == nil ||
				!strings.Contains(err.Error(), testCase.want) {
				t.Fatalf("inspect error = %v", err)
			}
		})
	}
}

func TestRunUninstallPreservesUnprovenMachineState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "daemon-home")
	userHome := filepath.Join(t.TempDir(), "user-home")
	if err := os.MkdirAll(userHome, 0o700); err != nil {
		t.Fatal(err)
	}
	var reportedStage, reportedDetail string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reportedStage = r.URL.Query().Get("stage")
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Error(err)
		}
		reportedDetail = string(body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	config := testUninstallConfig()
	config.APIURL = server.URL
	writeTestDaemonConfig(t, home, config)
	machine, err := localstore.Machine(home, config.InstallationID, config.MachineID)
	if err != nil {
		t.Fatal(err)
	}
	store, err := statedb.Open(
		context.Background(),
		machine.StateDBPath(),
		config.InstallationID,
		config.MachineID,
	)
	if err != nil {
		t.Fatal(err)
	}
	process := statedb.Process{
		ProcessID:            "prc-a",
		SupervisorInstanceID: "supervisor-a",
		SupervisorToken:      "token-a",
	}
	if err := store.ReserveProcess(context.Background(), process); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkPrepared(context.Background(), process.ProcessID, process.SupervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkAccepted(context.Background(), process.ProcessID, process.SupervisorInstanceID); err != nil {
		t.Fatal(err)
	}
	supervisor, err := statedb.OpenSupervisor(
		context.Background(),
		machine.StateDBPath(),
		config.InstallationID,
		config.MachineID,
		process.ProcessID,
		process.SupervisorInstanceID,
		process.SupervisorToken,
	)
	if err != nil {
		t.Fatal(err)
	}
	if execute, err := supervisor.AuthorizeSpawnOnce(context.Background()); err != nil || !execute {
		t.Fatalf("authorize spawn: execute=%t error=%v", execute, err)
	}
	if err := supervisor.RecordSpawned(context.Background(), "process_group", "123"); err != nil {
		t.Fatal(err)
	}
	if err := supervisor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := store.Close(); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", userHome)
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("PATH", t.TempDir())
	var stderr strings.Builder
	if code := Run(
		context.Background(),
		[]string{"uninstall", "--yes"},
		nil,
		&strings.Builder{},
		&stderr,
		discardLogger(),
	); code != 1 {
		t.Fatalf("uninstall exit code = %d", code)
	}
	if !strings.Contains(stderr.String(), "containment closure was proved") {
		t.Fatalf("uninstall stderr = %q", stderr.String())
	}
	if _, err := os.Stat(machine.StateDBPath()); err != nil {
		t.Fatalf("machine state was removed: %v", err)
	}
	if reportedStage != "daemon_uninstall" ||
		!strings.Contains(reportedDetail, "containment closure was proved") {
		t.Fatalf("uninstall report stage=%q detail=%q", reportedStage, reportedDetail)
	}
}

func testUninstallConfig() daemonConfig {
	return daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         "https://app.omnara.com",
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "token-a",
		RunnerPath:     "/bin",
	}
}

func writeTestInstallReceipt(t *testing.T, home string) {
	t.Helper()
	if err := localstore.WriteJSONAtomic(filepath.Join(home, installReceiptFileName), installReceipt{
		SchemaVersion:      installReceiptVersion,
		InstallMethod:      installReceiptMethod,
		ReleaseManifestURL: "https://releases.omnara.test/omnarad/latest/linux-amd64.txt",
	}, 0o600); err != nil {
		t.Fatal(err)
	}
}
