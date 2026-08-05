package localstore

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const (
	DefaultDirName        = ".omnarad"
	InstallationsDirName  = "installations"
	MachinesDirName       = "machines"
	RunDirName            = "run"
	ProcessesDirName      = "processes"
	StateDBFileName       = "state.sqlite"
	LifetimeLockFileName  = "lifetime.lock"
	OutputBufferFileName  = "output.buf"
	SkillsDirName         = "skills"
	SkillRevisionsDirName = "revisions"
)

type Store struct {
	home string
}

type MachineStore struct {
	home           string
	installationID string
	machineID      string
}

func New(home string) (Store, error) {
	home, err := normalizeHome(home)
	if err != nil {
		return Store{}, err
	}
	return Store{home: home}, nil
}

func normalizeHome(home string) (string, error) {
	if home == "" {
		return "", errors.New("omnara home is required")
	}
	if !filepath.IsAbs(home) {
		return "", errors.New("omnara home must be absolute")
	}
	home = filepath.Clean(home)
	if filepath.Dir(home) == home {
		return "", errors.New("omnara home cannot be a filesystem root")
	}
	return home, nil
}

func ResolveHome() (string, error) {
	home, err := resolveHome(os.Getenv, os.UserHomeDir)
	if err != nil {
		return "", err
	}
	return normalizeHome(home)
}

func Machine(home, installationID, machineID string) (MachineStore, error) {
	store, err := New(home)
	if err != nil {
		return MachineStore{}, err
	}
	return store.Machine(installationID, machineID)
}

func (s Store) HomeDir() string {
	return s.home
}

func (s Store) DaemonLockPath() string {
	return filepath.Join(s.home, "daemon.lock")
}

func (s Store) Machine(installationID, machineID string) (MachineStore, error) {
	if err := validatePathID("installation_id", installationID); err != nil {
		return MachineStore{}, err
	}
	if err := validatePathID("machine_id", machineID); err != nil {
		return MachineStore{}, err
	}
	return MachineStore{home: s.home, installationID: installationID, machineID: machineID}, nil
}

// SkillsDir returns the parent directory for extracted skill trees.
func (m MachineStore) SkillsDir() string {
	return filepath.Join(m.MachineDir(), SkillsDirName)
}

// SkillDir returns the on-machine directory for one extracted skill revision.
func (m MachineStore) SkillDir(skillID, revisionID string) (string, error) {
	if err := validatePathID("skill_id", skillID); err != nil {
		return "", err
	}
	if err := validatePathID("skill_revision_id", revisionID); err != nil {
		return "", err
	}
	return filepath.Join(m.SkillsDir(), skillID, SkillRevisionsDirName, revisionID), nil
}

func (m MachineStore) MachineDir() string {
	return filepath.Join(m.home, InstallationsDirName, m.installationID, MachinesDirName, m.machineID)
}

func (m MachineStore) RunDir() string {
	return filepath.Join(m.MachineDir(), RunDirName)
}

func (m MachineStore) ProcessesDir() string {
	return filepath.Join(m.MachineDir(), ProcessesDirName)
}

func (m MachineStore) StateDBPath() string {
	return filepath.Join(m.MachineDir(), StateDBFileName)
}

func (m MachineStore) ProcessDir(processID string) (string, error) {
	if err := validatePathID("process_id", processID); err != nil {
		return "", err
	}
	return filepath.Join(m.ProcessesDir(), processID), nil
}

func (m MachineStore) LifetimeLockPath(processID string) (string, error) {
	if err := validatePathID("process_id", processID); err != nil {
		return "", err
	}
	return filepath.Join(m.ProcessesDir(), processID+"."+LifetimeLockFileName), nil
}

func (m MachineStore) OutputBufferPath(processID string) (string, error) {
	return m.processPath(processID, OutputBufferFileName)
}

func (m MachineStore) ControlEndpointPath(processID string) (string, error) {
	if err := validatePathID("process_id", processID); err != nil {
		return "", err
	}
	return filepath.Join(m.RunDir(), processID+".sock"), nil
}

func (m MachineStore) processPath(processID, name string) (string, error) {
	dir, err := m.ProcessDir(processID)
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, name), nil
}

func ReadPrivateFile(path string) ([]byte, error) {
	info, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("private file must not be a symlink: %s", path)
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("private file must be regular: %s", path)
	}
	if err := validatePrivateFile(info); err != nil {
		return nil, fmt.Errorf("validate private file %s: %w", path, err)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() { _ = file.Close() }()
	openedInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(info, openedInfo) {
		return nil, fmt.Errorf("private file changed while opening: %s", path)
	}
	if err := validatePrivateFile(openedInfo); err != nil {
		return nil, fmt.Errorf("validate private file %s: %w", path, err)
	}
	return io.ReadAll(file)
}

func HasDurableMachineState(home string) (bool, error) {
	path := filepath.Join(home, InstallationsDirName)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return false, fmt.Errorf("installations path must be a directory: %s", path)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return false, err
	}
	return len(entries) > 0, nil
}

func WriteJSONAtomic(path string, value any, perm os.FileMode) error {
	body, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')
	return WriteFileAtomic(path, body, perm)
}

func WriteFileAtomic(path string, body []byte, perm os.FileMode) error {
	_, err := writeFileAtomic(
		path,
		perm,
		true,
		func(file *os.File) error {
			_, err := file.Write(body)
			return err
		},
		SyncDir,
	)
	return err
}

func WriteFileAtomicPreservingParentMode(path string, body []byte, perm os.FileMode) error {
	_, err := writeFileAtomic(
		path,
		perm,
		false,
		func(file *os.File) error {
			_, err := file.Write(body)
			return err
		},
		SyncDir,
	)
	return err
}

// WriteFileAtomicFunc requires the callback to finish reading the old target.
// published remains true if rename succeeds but the directory sync fails.
func WriteFileAtomicFunc(
	path string,
	perm os.FileMode,
	write func(*os.File) error,
) (published bool, err error) {
	return writeFileAtomic(path, perm, true, write, SyncDir)
}

func writeFileAtomic(
	path string,
	perm os.FileMode,
	privateParent bool,
	write func(*os.File) error,
	syncDir func(string) error,
) (bool, error) {
	if path == "" {
		return false, errors.New("path is required")
	}
	if write == nil {
		return false, errors.New("file writer is required")
	}
	if syncDir == nil {
		return false, errors.New("directory sync is required")
	}
	if perm == 0 {
		perm = 0o600
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create parent dir: %w", err)
	}
	if err := validateDir(dir); err != nil {
		return false, err
	}
	if privateParent {
		if err := os.Chmod(dir, 0o700); err != nil {
			return false, fmt.Errorf("chmod parent dir: %w", err)
		}
	}
	tmp, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return false, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(tmpPath)
		}
	}()
	if err := tmp.Chmod(perm); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("chmod temp file: %w", err)
	}
	if err := write(tmp); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("write temp file: %w", err)
	}
	if err := SyncFile(tmp); err != nil {
		_ = tmp.Close()
		return false, fmt.Errorf("sync temp file: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return false, fmt.Errorf("close temp file: %w", err)
	}
	if err := os.Rename(tmpPath, path); err != nil {
		return false, fmt.Errorf("replace target: %w", err)
	}
	cleanup = false
	if err := syncDir(dir); err != nil {
		return true, err
	}
	return true, nil
}

func ensurePrivateDir(dir string) error {
	if err := validateDir(dir); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Clean(dir), 0o700); err != nil {
		return fmt.Errorf("chmod parent dir: %w", err)
	}
	return nil
}

// EnsurePrivateDir does not isolate against another process under the same user.
func EnsurePrivateDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create private dir: %w", err)
	}
	return ensurePrivateDir(dir)
}

func ValidatePrivateFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("private file must be a regular non-symlink: %s", path)
	}
	return validatePrivateFile(info)
}

func ValidatePrivateFileInfo(info os.FileInfo) error {
	if info == nil || !info.Mode().IsRegular() {
		return errors.New("private file must be regular")
	}
	return validatePrivateFile(info)
}

func SyncDir(path string) error {
	return syncDirBestEffort(path)
}

func SyncFile(file *os.File) error {
	if file == nil {
		return errors.New("file is required")
	}
	return syncFile(file)
}

func validateDir(dir string) error {
	clean := filepath.Clean(dir)
	info, err := os.Lstat(clean)
	if err != nil {
		return fmt.Errorf("stat parent dir: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("parent dir must not be a symlink: %s", dir)
	}
	if !info.IsDir() {
		return fmt.Errorf("parent path is not a directory: %s", dir)
	}
	return nil
}

func validatePathID(name, value string) error {
	if value == "" {
		return fmt.Errorf("%s is required", name)
	}
	if filepath.IsAbs(value) || strings.ContainsAny(value, `/\`) {
		return fmt.Errorf("%s must be a single path segment", name)
	}
	if value == "." || value == ".." || filepath.Clean(value) != value {
		return fmt.Errorf("%s must be a clean path segment", name)
	}
	return nil
}

func resolveHome(getenv func(string) string, userHomeDir func() (string, error)) (string, error) {
	if override := getenv("OMNARA_HOME"); override != "" {
		if strings.TrimSpace(override) != override || !filepath.IsAbs(override) ||
			filepath.Clean(override) != override {
			return "", errors.New("OMNARA_HOME must be an absolute clean path")
		}
		return override, nil
	}
	home, err := userHomeDir()
	if err != nil || home == "" {
		return "", errors.New("user home directory is required")
	}
	if strings.TrimSpace(home) != home || !filepath.IsAbs(home) {
		return "", errors.New("user home directory must be absolute")
	}
	return filepath.Join(home, DefaultDirName), nil
}
