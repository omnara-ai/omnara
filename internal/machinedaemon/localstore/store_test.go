package localstore

import (
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestResolveHome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     map[string]string
		home    string
		want    string
		wantErr bool
	}{
		{
			name: "override",
			env:  map[string]string{"OMNARA_HOME": "/custom/omnara"},
			home: "/home/alice",
			want: "/custom/omnara",
		},
		{
			name: "linux default",
			env:  map[string]string{"XDG_STATE_HOME": "/state"},
			home: "/home/alice",
			want: filepath.Join("/home/alice", ".omnarad"),
		},
		{
			name: "macos default",
			env:  map[string]string{},
			home: "/Users/alice",
			want: filepath.Join("/Users/alice", ".omnarad"),
		},
		{
			name:    "padded override",
			env:     map[string]string{"OMNARA_HOME": " /custom/omnara "},
			home:    "/home/alice",
			wantErr: true,
		},
		{
			name:    "unclean override",
			env:     map[string]string{"OMNARA_HOME": "/custom/../omnara"},
			home:    "/home/alice",
			wantErr: true,
		},
		{
			name: "trailing slash user home",
			env:  map[string]string{},
			home: "/home/alice/",
			want: filepath.Join("/home/alice", ".omnarad"),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := resolveHome(
				func(key string) string { return tt.env[key] },
				func() (string, error) { return tt.home, nil },
			)
			if tt.wantErr {
				if err == nil {
					t.Fatal("resolve home succeeded")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolve home: %v", err)
			}
			if got != filepath.Clean(tt.want) {
				t.Fatalf("home = %q, want %q", got, filepath.Clean(tt.want))
			}
		})
	}
}

func TestNewRejectsFilesystemRoot(t *testing.T) {
	root := filepath.VolumeName(t.TempDir()) + string(os.PathSeparator)
	if _, err := New(root); err == nil {
		t.Fatal("new store accepted a filesystem root")
	}
}

func TestMachinePaths(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "omnara"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	machine, err := store.Machine("ins_123", "mach_456")
	if err != nil {
		t.Fatalf("machine store: %v", err)
	}
	if got := machine.ProcessesDir(); got != filepath.Join(
		store.HomeDir(),
		"installations",
		"ins_123",
		"machines",
		"mach_456",
		"processes",
	) {
		t.Fatalf("processes dir = %q", got)
	}
	if got := store.DaemonLockPath(); got != filepath.Join(store.HomeDir(), "daemon.lock") {
		t.Fatalf("daemon lock path = %q", got)
	}
	if got := machine.StateDBPath(); got != filepath.Join(
		machine.MachineDir(),
		"state.sqlite",
	) {
		t.Fatalf("state db path = %q", got)
	}
	if got, err := machine.LifetimeLockPath("proc_789"); err != nil ||
		got != filepath.Join(machine.ProcessesDir(), "proc_789.lifetime.lock") {
		t.Fatalf("lifetime lock path = %q err=%v", got, err)
	}
	if got, err := machine.OutputBufferPath("proc_789"); err != nil ||
		got != filepath.Join(machine.ProcessesDir(), "proc_789", "output.buf") {
		t.Fatalf("output buffer path = %q err=%v", got, err)
	}
	if got, err := machine.ControlEndpointPath("proc_789"); err != nil ||
		got != filepath.Join(machine.RunDir(), "proc_789.sock") {
		t.Fatalf("control endpoint = %q err=%v", got, err)
	}
	if got, err := machine.SkillDir("skl_abc", "skr_def"); err != nil ||
		got != filepath.Join(
			store.HomeDir(),
			"installations",
			"ins_123",
			"machines",
			"mach_456",
			"skills",
			"skl_abc",
			"revisions",
			"skr_def",
		) {
		t.Fatalf("skill revision dir = %q err=%v", got, err)
	}
}

func TestPathIDsRejectTraversal(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "omnara"))
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`, filepath.Clean("a/..")} {
		if _, err := store.Machine("ins_123", bad); err == nil {
			t.Fatalf("expected bad machine id %q to fail", bad)
		}
	}
	machine, err := store.Machine("ins_123", "mach_456")
	if err != nil {
		t.Fatalf("machine store: %v", err)
	}
	for _, bad := range []string{"", ".", "..", "a/b", `a\b`} {
		if _, err := machine.ProcessDir(bad); err == nil {
			t.Fatalf("expected bad process id %q to fail", bad)
		}
		if _, err := machine.SkillDir(bad, "skr_valid"); err == nil {
			t.Fatalf("expected bad skill id %q to fail", bad)
		}
		if _, err := machine.SkillDir("skl_valid", bad); err == nil {
			t.Fatalf("expected bad skill revision id %q to fail", bad)
		}
	}
}

func TestWriteJSONAtomic(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state", "state.json")
	if err := WriteJSONAtomic(path, map[string]string{"key_1": "value_1"}, 0o600); err != nil {
		t.Fatalf("write json atomic: %v", err)
	}
	body, err := ReadPrivateFile(path)
	if err != nil {
		t.Fatalf("read json: %v", err)
	}
	var got map[string]string
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("decode json: %v", err)
	}
	if got["key_1"] != "value_1" {
		t.Fatalf("unexpected json: %+v", got)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat json: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("json file permissions too broad: %v", info.Mode().Perm())
	}
}

func TestWriteFileAtomicPreservingParentMode(t *testing.T) {
	t.Parallel()

	dir := filepath.Join(t.TempDir(), "shared")
	if err := os.Mkdir(dir, 0o755); err != nil {
		t.Fatalf("create shared directory: %v", err)
	}
	if err := WriteFileAtomicPreservingParentMode(filepath.Join(dir, "service"), []byte("service"), 0o644); err != nil {
		t.Fatalf("write file atomically: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat shared directory: %v", err)
	}
	if got := info.Mode().Perm(); got != 0o755 {
		t.Fatalf("shared directory mode = %o, want 755", got)
	}
}

func TestWriteFileAtomicReportsPublishedBeforeDirectorySync(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "state", "published")
	injected := errors.New("injected directory sync failure")
	published, err := writeFileAtomic(
		path,
		0o600,
		true,
		func(file *os.File) error {
			_, err := file.WriteString("new generation")
			return err
		},
		func(string) error { return injected },
	)
	if !published || !errors.Is(err, injected) {
		t.Fatalf("published=%t error=%v", published, err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read published target: %v", err)
	}
	if string(body) != "new generation" {
		t.Fatalf("published target = %q", body)
	}
}

func TestWriteFileAtomicRejectsSymlinkParent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if err := WriteFileAtomic(filepath.Join(link, "state.json"), []byte(`{}`), 0o600); err == nil {
		t.Fatal("expected symlink parent to be rejected")
	}
}

func TestReadPrivateFileRejectsSymlinkFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target.json")
	if err := os.WriteFile(target, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	link := filepath.Join(root, "manifest.json")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	if _, err := ReadPrivateFile(link); err == nil {
		t.Fatal("expected symlink private file to be rejected")
	}
}

func TestReadPrivateFileRejectsBroadPermissions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission contract")
	}
	path := filepath.Join(t.TempDir(), "daemon.json")
	if err := os.WriteFile(path, []byte(`{}`), 0o644); err != nil {
		t.Fatalf("write private file: %v", err)
	}
	if _, err := ReadPrivateFile(path); err == nil ||
		!strings.Contains(err.Error(), "permissions allow group or other access") {
		t.Fatalf("read private file error = %v, want permission rejection", err)
	}
}

func TestHasDurableMachineState(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	hasState, err := HasDurableMachineState(home)
	if err != nil || hasState {
		t.Fatalf("missing installations: has_state=%t error=%v", hasState, err)
	}
	installations := filepath.Join(home, InstallationsDirName)
	if err := os.Mkdir(installations, 0o700); err != nil {
		t.Fatalf("mkdir installations: %v", err)
	}
	hasState, err = HasDurableMachineState(home)
	if err != nil || hasState {
		t.Fatalf("empty installations: has_state=%t error=%v", hasState, err)
	}
	if err := os.Mkdir(filepath.Join(installations, "inst-a"), 0o700); err != nil {
		t.Fatalf("mkdir installation: %v", err)
	}
	hasState, err = HasDurableMachineState(home)
	if err != nil || !hasState {
		t.Fatalf("durable installations: has_state=%t error=%v", hasState, err)
	}
}

func TestTryAcquireLockCreatesRestrictedLockFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "locks", "daemon.lock")
	lock, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat lock: %v", err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("lock file permissions too broad: %v", info.Mode().Perm())
	}
}

func TestTryAcquireLockRejectsSymlinkParent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatalf("mkdir target: %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	lock, err := TryAcquireLock(filepath.Join(link, "daemon.lock"))
	if err == nil {
		_ = lock.Release()
		t.Fatal("expected symlink parent to be rejected")
	}
}

func TestTryAcquireLockRejectsSymlinkFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	target := filepath.Join(root, "target")
	want := []byte("preserve")
	if err := os.WriteFile(target, want, 0o600); err != nil {
		t.Fatalf("write target: %v", err)
	}
	path := filepath.Join(root, "daemon.lock")
	if err := os.Symlink(target, path); err != nil {
		t.Skipf("symlink unavailable: %v", err)
	}
	lock, err := TryAcquireLock(path)
	if err == nil {
		_ = lock.Release()
		t.Fatal("expected symlink lock file to be rejected")
	}
	got, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("read target: %v", err)
	}
	if string(got) != string(want) {
		t.Fatalf("target contents = %q, want %q", got, want)
	}
}

func TestTryAcquireLockRejectsContender(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "locks", "daemon.lock")
	first, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	defer func() { _ = first.Release() }()
	second, err := TryAcquireLock(path)
	if !errors.Is(err, ErrLockHeld) {
		if second != nil {
			_ = second.Release()
		}
		t.Fatalf("second lock error = %v, want ErrLockHeld", err)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("release first lock: %v", err)
	}
	third, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("third lock after release: %v", err)
	}
	if err := third.Release(); err != nil {
		t.Fatalf("release third lock: %v", err)
	}
}

func TestLockPIDInspection(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "locks", "daemon.lock")
	lock, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	if err := lock.WritePID(12345); err != nil {
		t.Fatalf("write lock PID: %v", err)
	}
	pid, held, err := InspectLock(path)
	if err != nil || !held || pid != 12345 {
		t.Fatalf("inspect held lock = pid %d held %t error %v", pid, held, err)
	}
	if err := lock.Release(); err != nil {
		t.Fatalf("release lock: %v", err)
	}
	pid, held, err = InspectLock(path)
	if err != nil || held || pid != 0 {
		t.Fatalf("inspect released lock = pid %d held %t error %v", pid, held, err)
	}
}

func TestLockPIDRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "locks", "daemon.lock")
	lock, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	defer func() { _ = lock.Release() }()
	if err := lock.WritePID(0); err == nil {
		t.Fatal("zero lock PID succeeded")
	}
	if err := os.WriteFile(path, []byte("invalid\n"), 0o600); err != nil {
		t.Fatalf("write invalid lock PID: %v", err)
	}
	if _, held, err := InspectLock(path); err == nil || !held {
		t.Fatalf("inspect invalid held lock = held %t error %v", held, err)
	}
}

func TestLockIsNotInheritedByChildProcess(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("lock inheritance semantics are covered with Windows handle flags")
	}
	t.Parallel()

	path := filepath.Join(t.TempDir(), "locks", "daemon.lock")
	lock, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("acquire lock: %v", err)
	}
	cmd := exec.Command("sleep", "1")
	if err := cmd.Start(); err != nil {
		_ = lock.Release()
		t.Fatalf("start child: %v", err)
	}
	defer func() { _ = cmd.Process.Kill() }()
	if err := lock.Release(); err != nil {
		t.Fatalf("release parent lock: %v", err)
	}
	reacquired, err := TryAcquireLock(path)
	if err != nil {
		t.Fatalf("lock should be reacquirable while child is alive: %v", err)
	}
	if err := reacquired.Release(); err != nil {
		t.Fatalf("release reacquired lock: %v", err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(1500 * time.Millisecond):
		t.Fatal("child did not exit")
	}
}
