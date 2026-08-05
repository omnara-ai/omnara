package omnarad

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

func TestCanonicalAPIURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   string
		want    string
		wantErr string
	}{
		{name: "https", value: " HTTPS://Example.COM:443/base/ ", want: "https://example.com/base"},
		{name: "loopback http", value: "http://127.0.0.1:8080/api/", want: "http://127.0.0.1:8080/api"},
		{name: "ipv6 loopback", value: "http://[::1]:80/", want: "http://[::1]"},
		{name: "docker host http", value: "http://host.docker.internal:8080/", want: "http://host.docker.internal:8080"},
		{name: "encoded base path", value: "https://example.com/a%2Fb/", want: "https://example.com/a%2Fb"},
		{name: "encoded trailing slash", value: "https://example.com/base%2F/", want: "https://example.com/base%2F"},
		{name: "non-loopback http", value: "http://example.com", wantErr: "must use https"},
		{name: "userinfo", value: "https://user@example.com", wantErr: "user info"},
		{name: "query", value: "https://example.com?x=1", wantErr: "query or fragment"},
		{name: "relative", value: "/api", wantErr: "absolute"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := canonicalAPIURL(tt.value)
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("canonical API URL error = %v, want %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("canonical API URL: %v", err)
			}
			if got != tt.want {
				t.Fatalf("canonical API URL = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWriteDaemonConfigWritesValidatedBinding(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
	defer server.Close()
	setDaemonEnvironment(t, home, server.URL+"/base/", "token-a")
	t.Setenv("PATH", "/ambient/bin:/usr/bin")

	err := writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger())
	if err != nil {
		t.Fatalf("configure daemon: %v", err)
	}
	config, err := loadDaemonConfig(home)
	if err != nil {
		t.Fatalf("load daemon config: %v", err)
	}
	if config == nil {
		t.Fatal("daemon config was not written")
	}
	want := daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         server.URL + "/base",
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "token-a",
		NoUpdate:       false,
		RunnerPath:     "/ambient/bin:/usr/bin",
	}
	if *config != want {
		t.Fatalf("daemon config = %+v, want %+v", *config, want)
	}
	info, err := os.Stat(filepath.Join(home, daemonConfigFileName))
	if err != nil {
		t.Fatalf("stat daemon config: %v", err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("daemon config mode = %o, want 600", info.Mode().Perm())
	}
	if err := writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger()); err != nil {
		t.Fatalf("reconfigure unchanged daemon: %v", err)
	}
	after, err := os.Stat(filepath.Join(home, daemonConfigFileName))
	if err != nil {
		t.Fatalf("stat unchanged daemon config: %v", err)
	}
	if !os.SameFile(info, after) {
		t.Fatal("unchanged configuration replaced daemon.json")
	}
}

func TestWriteDaemonConfigUpdatesAPIURLAfterValidatingExistingBinding(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	originalRequests := 0
	original := bootstrapServer(t, "stored-token", "inst-a", "mch-a", func() { originalRequests++ })
	defer original.Close()
	proposedRequests := 0
	proposed := bootstrapServer(t, "stored-token", "inst-a", "mch-a", func() { proposedRequests++ })
	defer proposed.Close()
	writeTestDaemonConfig(t, home, daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         original.URL,
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "stored-token",
		RunnerPath:     "/stored/bin",
	})
	setDaemonEnvironment(t, home, proposed.URL, "")

	if err := writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger()); err != nil {
		t.Fatalf("configure daemon with API URL override: %v", err)
	}
	if originalRequests != 0 || proposedRequests != 1 {
		t.Fatalf("bootstrap requests = original %d proposed %d, want 0 and 1", originalRequests, proposedRequests)
	}
	config, err := loadDaemonConfig(home)
	if err != nil {
		t.Fatalf("load updated daemon config: %v", err)
	}
	if config.APIURL != proposed.URL || config.MachineToken != "stored-token" ||
		config.InstallationID != "inst-a" || config.MachineID != "mch-a" {
		t.Fatalf("updated daemon config = %+v", *config)
	}
}

func TestWriteDaemonConfigRotatesTokenForSameBinding(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server := bootstrapServer(t, "new-token", "inst-a", "mch-a")
	defer server.Close()
	writeTestDaemonConfig(t, home, daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         server.URL,
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "old-token",
		NoUpdate:       true,
		RunnerPath:     "/stored/bin",
	})
	setDaemonEnvironment(t, home, server.URL, "new-token")
	t.Setenv("PATH", "/incidental/bin")
	t.Setenv("OMNARA_NO_UPDATE", "0")

	if err := writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger()); err != nil {
		t.Fatalf("configure daemon: %v", err)
	}
	config, err := loadDaemonConfig(home)
	if err != nil {
		t.Fatalf("load daemon config: %v", err)
	}
	if config.MachineToken != "new-token" {
		t.Fatalf("machine token = %q, want rotated token", config.MachineToken)
	}
	if config.RunnerPath != "/stored/bin" {
		t.Fatalf("runner path = %q, want persisted path", config.RunnerPath)
	}
	if config.NoUpdate {
		t.Fatal("no_update was not cleared by explicit 0")
	}
	t.Setenv("OMNARA_RUNNER_PATH", "/explicit/bin")
	if err := writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger()); err != nil {
		t.Fatalf("configure daemon with runner override: %v", err)
	}
	config, err = loadDaemonConfig(home)
	if err != nil {
		t.Fatalf("load daemon config after runner override: %v", err)
	}
	if config.RunnerPath != "/explicit/bin" {
		t.Fatalf("runner path = %q, want explicit override", config.RunnerPath)
	}
}

func TestWriteDaemonConfigRejectsBindingChange(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	originalRequests := 0
	original := bootstrapServer(t, "old-token", "inst-a", "mch-a", func() { originalRequests++ })
	defer original.Close()
	proposedRequests := 0
	proposed := bootstrapServer(t, "new-token", "inst-a", "mch-b", func() { proposedRequests++ })
	defer proposed.Close()
	config := daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         original.URL,
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "old-token",
		RunnerPath:     "/stored/bin",
	}
	writeTestDaemonConfig(t, home, config)
	before, err := os.ReadFile(filepath.Join(home, daemonConfigFileName))
	if err != nil {
		t.Fatalf("read config before: %v", err)
	}
	setDaemonEnvironment(t, home, proposed.URL, "new-token")

	err = writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "does not match configured machine") {
		t.Fatalf("configure daemon error = %v, want binding mismatch", err)
	}
	if originalRequests != 0 || proposedRequests != 1 {
		t.Fatalf("bootstrap requests = original %d proposed %d, want 0 and 1", originalRequests, proposedRequests)
	}
	after, err := os.ReadFile(filepath.Join(home, daemonConfigFileName))
	if err != nil {
		t.Fatalf("read config after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("binding mismatch changed daemon config")
	}
}

func TestWriteDaemonConfigRejectsInvalidAuthAtAPIURLOverride(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected", http.StatusUnauthorized)
	}))
	defer server.Close()
	writeTestDaemonConfig(t, home, daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         "https://original.example.com",
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "stored-token",
		RunnerPath:     "/stored/bin",
	})
	before, err := os.ReadFile(filepath.Join(home, daemonConfigFileName))
	if err != nil {
		t.Fatalf("read config before: %v", err)
	}
	setDaemonEnvironment(t, home, server.URL, "")

	err = writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger())
	want := "OMNARA_MACHINE_TOKEN was rejected by " + server.URL
	if err == nil || err.Error() != want {
		t.Fatalf("configure daemon error = %v, want %q", err, want)
	}
	after, err := os.ReadFile(filepath.Join(home, daemonConfigFileName))
	if err != nil {
		t.Fatalf("read config after: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("rejected API URL override changed daemon config")
	}
}

func TestWriteDaemonConfigRejectsInvalidAuthWithoutWriting(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "rejected", http.StatusUnauthorized)
	}))
	defer server.Close()
	setDaemonEnvironment(t, home, server.URL, "bad-token")

	err := writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger())
	want := "OMNARA_MACHINE_TOKEN was rejected by " + server.URL
	if err == nil || err.Error() != want {
		t.Fatalf("configure daemon error = %v, want %q", err, want)
	}
	if _, err := os.Stat(filepath.Join(home, daemonConfigFileName)); !os.IsNotExist(err) {
		t.Fatalf("daemon config exists after rejected auth: %v", err)
	}
}

func TestWriteDaemonConfigRejectsInvalidNoUpdateBeforeRequest(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
	}))
	defer server.Close()
	setDaemonEnvironment(t, home, server.URL, "token")
	t.Setenv("OMNARA_NO_UPDATE", "true")

	err := writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger())
	if err == nil || err.Error() != "OMNARA_NO_UPDATE must be 0 or 1" {
		t.Fatalf("configure daemon error = %v, want invalid no-update value", err)
	}
	if requests != 0 {
		t.Fatalf("bootstrap requests = %d, want 0", requests)
	}
}

func TestWriteDaemonConfigRequiresAPIURLBeforeDurableRecovery(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	machine, err := localstore.Machine(home, "inst-a", "mch-a")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(machine.MachineDir(), 0o700); err != nil {
		t.Fatalf("create durable state: %v", err)
	}
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("OMNARA_API_URL", "")
	t.Setenv("OMNARA_MACHINE_TOKEN", "self-hosted-token")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = writeDaemonConfig(ctx, nil, io.Discard, discardLogger())
	if err == nil || !strings.Contains(err.Error(), "OMNARA_API_URL is required") {
		t.Fatalf("configure daemon error = %v, want explicit API URL requirement", err)
	}
}

func TestWriteDaemonConfigRecoversOnlyMatchingDurableState(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		machine string
		wantErr bool
	}{
		{name: "matching", machine: "mch-a"},
		{name: "different", machine: "mch-b", wantErr: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			home := filepath.Join(t.TempDir(), "home")
			machine, err := localstore.Machine(home, "inst-a", testCase.machine)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.MkdirAll(machine.MachineDir(), 0o700); err != nil {
				t.Fatalf("create durable state: %v", err)
			}
			server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
			defer server.Close()
			setDaemonEnvironment(t, home, server.URL, "token-a")

			err = writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger())
			if testCase.wantErr {
				if err == nil || !strings.Contains(err.Error(), "does not match existing durable state") {
					t.Fatalf("configure daemon error = %v, want durable-state mismatch", err)
				}
				return
			}
			if err != nil {
				t.Fatalf("recover daemon config: %v", err)
			}
			if _, err := loadDaemonConfig(home); err != nil {
				t.Fatalf("load recovered daemon config: %v", err)
			}
		})
	}
}

func TestWriteDaemonConfigTreatsCompleteStateDeletionAsFreshBootstrap(
	t *testing.T,
) {
	home := filepath.Join(t.TempDir(), "home")
	oldServer := bootstrapServer(t, "old-token", "inst-old", "mch-old")
	t.Cleanup(oldServer.Close)
	setDaemonEnvironment(t, home, oldServer.URL, "old-token")
	if err := writeDaemonConfig(
		context.Background(),
		nil,
		io.Discard,
		discardLogger(),
	); err != nil {
		t.Fatalf("configure original daemon: %v", err)
	}
	oldServer.Close()
	oldMachine, err := localstore.Machine(home, "inst-old", "mch-old")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(oldMachine.MachineDir(), 0o700); err != nil {
		t.Fatal(err)
	}
	oldArtifact := filepath.Join(oldMachine.MachineDir(), "old-state")
	if err := os.WriteFile(oldArtifact, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.RemoveAll(home); err != nil {
		t.Fatalf("remove complete daemon state: %v", err)
	}
	newServer := bootstrapServer(t, "new-token", "inst-new", "mch-new")
	defer newServer.Close()
	setDaemonEnvironment(t, home, newServer.URL, "new-token")
	if err := writeDaemonConfig(
		context.Background(),
		nil,
		io.Discard,
		discardLogger(),
	); err != nil {
		t.Fatalf("configure fresh daemon after state deletion: %v", err)
	}
	config, err := loadDaemonConfig(home)
	if err != nil {
		t.Fatal(err)
	}
	if config.InstallationID != "inst-new" ||
		config.MachineID != "mch-new" ||
		config.MachineToken != "new-token" {
		t.Fatalf("fresh daemon config = %+v", *config)
	}
	if _, err := os.Stat(oldArtifact); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("old local state survived complete reset: %v", err)
	}
}

func TestWriteDaemonConfigDoesNotUseInstallLock(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
	defer server.Close()
	setDaemonEnvironment(t, home, server.URL, "token-a")
	lock, err := acquireInstallLock(context.Background(), home)
	if err != nil {
		t.Fatalf("acquire install lock: %v", err)
	}
	defer func() {
		if err := lock.Release(); err != nil {
			t.Fatalf("release install lock: %v", err)
		}
	}()
	err = writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger())
	if err != nil {
		t.Fatalf("configure daemon while install lock is held: %v", err)
	}
}

func TestLoadDaemonConfigRejectsUnsafeOrInvalidFiles(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		mode    os.FileMode
		wantErr string
	}{
		{
			name: "unknown field",
			body: `{"schema_version":1,"api_url":"https://app.omnara.com",` +
				`"installation_id":"inst-a","machine_id":"mch-a","machine_token":"token",` +
				`"no_update":false,"runner_path":"/bin","extra":true}`,
			mode:    0o600,
			wantErr: "unknown field",
		},
		{
			name: "unknown schema",
			body: `{"schema_version":2,"api_url":"https://app.omnara.com",` +
				`"installation_id":"inst-a","machine_id":"mch-a","machine_token":"token",` +
				`"no_update":false,"runner_path":"/bin"}`,
			mode:    0o600,
			wantErr: "unsupported daemon config schema_version 2",
		},
		{
			name: "missing field",
			body: `{"schema_version":1,"api_url":"https://app.omnara.com",` +
				`"installation_id":"inst-a","machine_id":"mch-a","machine_token":"token",` +
				`"no_update":false}`,
			mode:    0o600,
			wantErr: "missing required fields",
		},
		{
			name: "broad permissions",
			body: `{"schema_version":1,"api_url":"https://app.omnara.com",` +
				`"installation_id":"inst-a","machine_id":"mch-a","machine_token":"token",` +
				`"no_update":false,"runner_path":"/bin"}`,
			mode:    0o644,
			wantErr: "permissions allow group or other access",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			path := filepath.Join(home, daemonConfigFileName)
			if err := os.WriteFile(path, []byte(tt.body), tt.mode); err != nil {
				t.Fatalf("write daemon config: %v", err)
			}
			if err := os.Chmod(path, tt.mode); err != nil {
				t.Fatalf("chmod daemon config: %v", err)
			}
			_, err := loadDaemonConfig(home)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("load daemon config error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestLoadDaemonConfigRejectsSymlink(t *testing.T) {
	home := t.TempDir()
	target := filepath.Join(t.TempDir(), "daemon.json")
	writeTestDaemonConfig(t, filepath.Dir(target), daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         defaultAPIURL,
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "token",
		RunnerPath:     "/bin",
	})
	if err := os.Symlink(target, filepath.Join(home, daemonConfigFileName)); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	_, err := loadDaemonConfig(home)
	if err == nil || !strings.Contains(err.Error(), "must not be a symlink") {
		t.Fatalf("load daemon config error = %v, want symlink rejection", err)
	}
}

func TestLoadRuntimeConfigAppliesTemporaryEnvironment(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	writeTestDaemonConfig(t, home, daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         "https://stored.example.com/base",
		InstallationID: "inst-stored",
		MachineID:      "mch-stored",
		MachineToken:   "stored-token",
		NoUpdate:       true,
		RunnerPath:     "/stored/bin",
	})
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("OMNARA_API_URL", "https://other.example.com")
	t.Setenv("OMNARA_MACHINE_TOKEN", "environment-token")
	t.Setenv("OMNARA_NO_UPDATE", "0")
	t.Setenv("OMNARA_RUNNER_PATH", "/environment/bin")

	persisted, persistedNoUpdate, overridden, err := loadRuntimeConfig(false)
	if err != nil {
		t.Fatalf("load persisted runtime config: %v", err)
	}
	if persisted.APIURL != "https://stored.example.com/base" ||
		persisted.MachineToken != "stored-token" || persisted.RunnerPath != "/stored/bin" ||
		!persistedNoUpdate || overridden {
		t.Fatalf(
			"persisted runtime config = %+v no_update=%t overridden=%t",
			persisted,
			persistedNoUpdate,
			overridden,
		)
	}

	config, noUpdate, overridden, err := loadRuntimeConfig(true)
	if err != nil {
		t.Fatalf("load runtime config: %v", err)
	}
	if config.APIURL != "https://other.example.com" || config.MachineToken != "environment-token" ||
		config.DaemonVersion != version ||
		config.ExpectedInstallationID != "inst-stored" || config.ExpectedMachineID != "mch-stored" ||
		config.RunnerPath != "/environment/bin" {
		t.Fatalf("runtime config = %+v", config)
	}
	if noUpdate {
		t.Fatal("runtime update policy ignored environment override")
	}
	if !overridden {
		t.Fatal("runtime environment override was not detected")
	}
}

func TestApplyRuntimeEnvironmentSleepSettings(t *testing.T) {
	t.Setenv(daemonprotocol.SleepAfterEnvVar, "5000")
	if err := applyRuntimeEnvironment(&machinedaemon.Config{}); err == nil {
		t.Fatal("sleep_after below the floor must be rejected")
	}

	t.Setenv(daemonprotocol.SleepAfterEnvVar, "30000")
	t.Setenv(daemonprotocol.SleepPlatformEnvVar, daemonprotocol.SleepPlatformUnikraft)
	config := machinedaemon.Config{}
	if err := applyRuntimeEnvironment(&config); err != nil {
		t.Fatalf("apply runtime environment: %v", err)
	}
	if config.SleepAfter != 30*time.Second ||
		config.SleepPlatform != daemonprotocol.SleepPlatformUnikraft {
		t.Fatalf("sleep config = %v/%q, want 30s/unikraft", config.SleepAfter, config.SleepPlatform)
	}
	defaultWakeListen := ":" + strconv.Itoa(daemonprotocol.WakeListenerPort)
	if config.WakeListenAddr != defaultWakeListen {
		t.Fatalf("wake listen addr = %q, want default %q", config.WakeListenAddr, defaultWakeListen)
	}

	t.Setenv(daemonprotocol.WakeListenAddrEnvVar, ":9000")
	config = machinedaemon.Config{}
	if err := applyRuntimeEnvironment(&config); err != nil {
		t.Fatalf("apply runtime environment with explicit wake address: %v", err)
	}
	if config.WakeListenAddr != ":9000" {
		t.Fatalf("wake listen addr = %q, want :9000", config.WakeListenAddr)
	}

	t.Setenv(
		daemonprotocol.SleepAfterEnvVar,
		strconv.FormatInt(daemonprotocol.MaximumSleepAfterMS, 10),
	)
	config = machinedaemon.Config{}
	if err := applyRuntimeEnvironment(&config); err != nil {
		t.Fatalf("maximum sleep_after rejected: %v", err)
	}
	if config.SleepAfter != time.Duration(daemonprotocol.MaximumSleepAfterMS)*time.Millisecond {
		t.Fatalf("maximum sleep_after = %v", config.SleepAfter)
	}

	t.Setenv(
		daemonprotocol.SleepAfterEnvVar,
		strconv.FormatInt(daemonprotocol.MaximumSleepAfterMS+1, 10),
	)
	if err := applyRuntimeEnvironment(&machinedaemon.Config{}); err == nil {
		t.Fatal("sleep_after above the duration limit must be rejected")
	}
}

func TestRunVersionHelpAndUsage(t *testing.T) {
	var stdout strings.Builder
	var stderr strings.Builder
	if code := Run(context.Background(), []string{"--version"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("version exit code = %d", code)
	}
	if stdout.String() != version+"\n" || stderr.Len() != 0 {
		t.Fatalf("version stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	stdout.Reset()
	if code := Run(context.Background(), []string{"--help"}, nil, &stdout, &stderr, discardLogger()); code != 0 {
		t.Fatalf("help exit code = %d", code)
	}
	if help := stdout.String(); !strings.Contains(help, "Usage: omnarad") ||
		strings.Contains(help, "run-service") || strings.Contains(help, "__omnara_process_runner") {
		t.Fatalf("help output = %q", help)
	}
	stdout.Reset()
	if code := Run(context.Background(), []string{"unknown"}, nil, &stdout, &stderr, discardLogger()); code != 1 {
		t.Fatalf("invalid command exit code = %d", code)
	}
	const usage = "error: invalid subcommand: unknown\nUsage: omnarad [--version] <command> [<args>]\n"
	if stderr.String() != usage {
		t.Fatalf("invalid command stderr = %q, want %q", stderr.String(), usage)
	}
}

func TestRunNoServiceBypassesServiceManager(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	server := bootstrapServer(t, "token-a", "inst-a", "mch-a")
	defer server.Close()
	setConfiguredDaemonEnvironment(t, home, server.URL, "/bin")
	configBefore, err := os.ReadFile(filepath.Join(home, daemonConfigFileName))
	if err != nil {
		t.Fatalf("read daemon config before start: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(home, "bin"), 0o700); err != nil {
		t.Fatalf("create daemon bin: %v", err)
	}
	writeTestExecutable(t, canonicalDaemonPath(home), "#!/bin/sh\nexit 0\n")
	commandDir := t.TempDir()
	writeTestExecutable(t, filepath.Join(commandDir, "launchctl"), "#!/bin/sh\necho 'Could not find domain' >&2\nexit 3\n")
	writeTestExecutable(
		t,
		filepath.Join(commandDir, "systemctl"),
		"#!/bin/sh\necho 'Failed to connect to bus: No medium found' >&2\nexit 1\n",
	)
	t.Setenv("PATH", commandDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	var stdout strings.Builder
	var stderr strings.Builder
	if code := Run(
		context.Background(), []string{"start", "--no-service"}, nil, &stdout, &stderr, discardLogger(),
	); code != 0 {
		t.Fatalf("no-service exit code = %d", code)
	}
	if stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("no-service stdout=%q stderr=%q", stdout.String(), stderr.String())
	}
	configAfter, err := os.ReadFile(filepath.Join(home, daemonConfigFileName))
	if err != nil {
		t.Fatalf("read daemon config after start: %v", err)
	}
	if string(configAfter) != string(configBefore) {
		t.Fatal("start changed daemon config")
	}
}

func TestRunServiceRejectsInvalidRetryInterval(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	setConfiguredDaemonEnvironment(t, home, "https://example.com", "/bin")
	t.Setenv("OMNARA_DAEMON_RETRY_INTERVAL_MS", "invalid")
	err := runService(context.Background(), discardLogger(), false)
	if err == nil || err.Error() != "OMNARA_DAEMON_RETRY_INTERVAL_MS must be positive integer milliseconds" {
		t.Fatalf("run service error = %v", err)
	}
}

func TestWriteDaemonConfigDoesNotUseRuntimeLock(t *testing.T) {
	home := t.TempDir()
	server := bootstrapServer(t, "new-token", "inst-a", "mch-a")
	defer server.Close()
	existing := daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         server.URL,
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "old-token",
		RunnerPath:     "/bin",
	}
	writeTestDaemonConfig(t, home, existing)
	setDaemonEnvironment(t, home, server.URL, "new-token")
	store, err := localstore.New(home)
	if err != nil {
		t.Fatalf("open local store: %v", err)
	}
	lock, err := localstore.TryAcquireLock(store.DaemonLockPath())
	if err != nil {
		t.Fatalf("acquire daemon lock: %v", err)
	}
	t.Cleanup(func() { _ = lock.Release() })

	if err := writeDaemonConfig(context.Background(), nil, io.Discard, discardLogger()); err != nil {
		t.Fatalf("configure daemon while runtime lock is held: %v", err)
	}
	config, err := loadDaemonConfig(home)
	if err != nil {
		t.Fatalf("load daemon config: %v", err)
	}
	if config.MachineToken != "new-token" {
		t.Fatalf("machine token = %q, want new-token", config.MachineToken)
	}
}

func bootstrapServer(
	t *testing.T,
	token string,
	installationID string,
	machineID string,
	afterResponse ...func(),
) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/api/v1/daemon/bootstrap") {
			http.NotFound(w, r)
			return
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+token {
			http.Error(w, "bad token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"installation_id": installationID,
			"machine_id":      machineID,
		})
		for _, fn := range afterResponse {
			fn()
		}
	}))
}

func writeTestDaemonConfig(t *testing.T, home string, config daemonConfig) {
	t.Helper()
	if err := localstore.WriteJSONAtomic(filepath.Join(home, daemonConfigFileName), config, 0o600); err != nil {
		t.Fatalf("write daemon config: %v", err)
	}
}

func setDaemonEnvironment(t *testing.T, home, apiURL, token string) {
	t.Helper()
	t.Setenv("OMNARA_HOME", home)
	t.Setenv("OMNARA_API_URL", apiURL)
	t.Setenv("OMNARA_MACHINE_TOKEN", token)
	t.Setenv("OMNARA_NO_UPDATE", "0")
	t.Setenv("OMNARA_RUNNER_PATH", "")
}

func setConfiguredDaemonEnvironment(t *testing.T, home, apiURL, runnerPath string) {
	t.Helper()
	setDaemonEnvironment(t, home, apiURL, "token-a")
	writeTestDaemonConfig(t, home, daemonConfig{
		SchemaVersion:  daemonConfigVersion,
		APIURL:         apiURL,
		InstallationID: "inst-a",
		MachineID:      "mch-a",
		MachineToken:   "token-a",
		RunnerPath:     runnerPath,
	})
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}
