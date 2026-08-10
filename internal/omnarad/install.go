package omnarad

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

const (
	installReceiptFileName      = "install.json"
	installReceiptVersion       = 1
	installReceiptMethod        = "omnarad.sh"
	installRepairStagingDirName = ".omnarad-repair"
)

type installReceipt struct {
	SchemaVersion      int    `json:"schema_version"`
	InstallMethod      string `json:"install_method"`
	ReleaseManifestURL string `json:"release_manifest_url"`
}

type daemonInstallResult struct {
	path     string
	replaced bool
}

func runInstallCommand(
	ctx context.Context,
	releaseManifestURL string,
	noStart bool,
	stdin *os.File,
	stdout io.Writer,
	stderr io.Writer,
	log *slog.Logger,
) int {
	home, err := localstore.ResolveHome()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	executable, err := os.Executable()
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := validateInstallInputs(executable, releaseManifestURL); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if err := localstore.EnsurePrivateDir(home); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	lock, err := acquireInstallLock(ctx, home)
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	result, installErr := installDaemonLocked(ctx, home, executable, releaseManifestURL)
	if installErr == nil {
		installErr = writeDaemonConfig(ctx, stdin, stderr, log)
	}
	err = errors.Join(installErr, lock.Release())
	if err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	if result.replaced {
		_, _ = fmt.Fprintln(stdout, "omnarad installed at "+result.path)
	}
	if noStart {
		return 0
	}
	if err := reexecDaemon(ctx, result.path, restartSubcommand); err != nil {
		_, _ = fmt.Fprintln(stderr, err)
		return 1
	}
	return 0
}

func installDaemon(
	ctx context.Context,
	home string,
	executable string,
	releaseManifestURL string,
) (result daemonInstallResult, resultErr error) {
	if err := validateInstallInputs(executable, releaseManifestURL); err != nil {
		return daemonInstallResult{}, err
	}
	if err := localstore.EnsurePrivateDir(home); err != nil {
		return daemonInstallResult{}, err
	}
	lock, err := acquireInstallLock(ctx, home)
	if err != nil {
		return daemonInstallResult{}, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, lock.Release())
	}()
	return installDaemonLocked(ctx, home, executable, releaseManifestURL)
}

func installDaemonLocked(
	ctx context.Context,
	home string,
	executable string,
	releaseManifestURL string,
) (daemonInstallResult, error) {
	canonical := canonicalDaemonPath(home)
	receipt, err := loadInstallReceipt(home)
	if errors.Is(err, os.ErrNotExist) {
		if releaseManifestURL == "" {
			return daemonInstallResult{}, errors.New(
				"--release-manifest-url is required for a new installation",
			)
		}
		empty, inspectErr := installHomeEmptyLocked(home)
		if inspectErr != nil {
			return daemonInstallResult{}, inspectErr
		}
		if !empty {
			return daemonInstallResult{}, fmt.Errorf(
				"cannot install into %s because it contains unrecognized installation state",
				home,
			)
		}
		receipt = installReceipt{
			SchemaVersion:      installReceiptVersion,
			InstallMethod:      installReceiptMethod,
			ReleaseManifestURL: releaseManifestURL,
		}
		receiptPath := filepath.Join(home, installReceiptFileName)
		if err := localstore.WriteJSONAtomic(receiptPath, receipt, 0o600); err != nil {
			return daemonInstallResult{}, fmt.Errorf("write install receipt: %w", err)
		}
		if err := replaceCanonicalDaemon(executable, canonical); err != nil {
			rollbackErr := os.Remove(filepath.Dir(canonical))
			if errors.Is(rollbackErr, os.ErrNotExist) {
				rollbackErr = nil
			}
			if rollbackErr == nil {
				rollbackErr = os.Remove(receiptPath)
			}
			return daemonInstallResult{}, errors.Join(err, rollbackErr)
		}
		return daemonInstallResult{path: canonical, replaced: true}, nil
	}
	if err != nil {
		return daemonInstallResult{}, fmt.Errorf("inspect install receipt: %w", err)
	}
	usable, err := canonicalDaemonUsable(ctx, canonical)
	if err != nil {
		return daemonInstallResult{}, err
	}
	if usable {
		return daemonInstallResult{path: canonical}, nil
	}
	if err := repairCanonicalDaemon(ctx, home, canonical, receipt); err != nil {
		return daemonInstallResult{}, err
	}
	return daemonInstallResult{path: canonical, replaced: true}, nil
}

func validateInstallInputs(executable, releaseManifestURL string) error {
	if releaseManifestURL != "" {
		if err := validateReleaseManifestURL(releaseManifestURL); err != nil {
			return fmt.Errorf("release manifest URL: %w", err)
		}
	}
	return validateInstallSource(executable)
}

func repairCanonicalDaemon(ctx context.Context, home, canonical string, receipt installReceipt) error {
	var body bytes.Buffer
	if err := downloadPublicRelease(ctx, receipt.ReleaseManifestURL, updateManifestMaxBytes, &body); err != nil {
		return fmt.Errorf("download repair manifest: %w", err)
	}
	manifest, err := parseReleaseManifest(body.Bytes())
	if err != nil {
		return err
	}
	if err := createOwnedDirectory(filepath.Dir(canonical), 0o700); err != nil {
		return err
	}
	staged, err := stageDaemonRelease(
		ctx,
		home,
		daemonUpdate{manifest: manifest},
		installRepairStagingDirName,
	)
	if err != nil {
		return err
	}
	defer func() { _ = os.RemoveAll(staged.dir) }()
	return replaceCanonicalDaemon(staged.path, canonical)
}

func validateInstallSource(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect staged omnarad: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("staged omnarad must be a regular file: %s", path)
	}
	return nil
}

func installHomeEmptyLocked(home string) (bool, error) {
	entries, err := os.ReadDir(home)
	if err != nil {
		return false, fmt.Errorf("inspect daemon home: %w", err)
	}
	for _, entry := range entries {
		if entry.Name() != installLockFileName {
			return false, nil
		}
	}
	return true, nil
}

func canonicalDaemonUsable(ctx context.Context, path string) (bool, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return false, fmt.Errorf("canonical omnarad path must be a regular file: %s", path)
	}
	if info.Mode().Perm()&0o077 != 0 || info.Mode().Perm()&0o100 == 0 {
		return false, nil
	}
	owned := ensureCurrentUserOwner(info, path) == nil
	if !owned {
		return false, nil
	}
	_, versionErr := readExecutableVersion(ctx, path)
	return versionErr == nil, nil
}

func replaceCanonicalDaemon(source, destination string) error {
	sourceInfo, err := os.Lstat(source)
	if err != nil {
		return fmt.Errorf("inspect staged omnarad: %w", err)
	}
	sourceFile, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open staged omnarad: %w", err)
	}
	defer func() { _ = sourceFile.Close() }()
	openedInfo, err := sourceFile.Stat()
	if err != nil {
		return fmt.Errorf("inspect opened staged omnarad: %w", err)
	}
	if !os.SameFile(sourceInfo, openedInfo) {
		return errors.New("staged omnarad changed while opening")
	}

	binDir := filepath.Dir(destination)
	if err := createOwnedDirectory(binDir, 0o700); err != nil {
		return err
	}
	if info, err := os.Lstat(destination); err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
			return fmt.Errorf("canonical omnarad path must be a regular file: %s", destination)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}

	temporary, err := os.CreateTemp(binDir, ".omnarad-install-*")
	if err != nil {
		return fmt.Errorf("create staged canonical omnarad: %w", err)
	}
	temporaryPath := temporary.Name()
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.Remove(temporaryPath)
		}
	}()
	if err := temporary.Chmod(0o700); err != nil {
		_ = temporary.Close()
		return fmt.Errorf("chmod staged canonical omnarad: %w", err)
	}
	_, copyErr := io.Copy(temporary, sourceFile)
	syncErr := temporary.Sync()
	closeErr := temporary.Close()
	if err := errors.Join(copyErr, syncErr, closeErr); err != nil {
		return fmt.Errorf("copy staged omnarad: %w", err)
	}
	if err := os.Rename(temporaryPath, destination); err != nil {
		return fmt.Errorf("replace canonical omnarad: %w", err)
	}
	cleanup = false
	return nil
}

func loadInstallReceipt(home string) (installReceipt, error) {
	path := filepath.Join(home, installReceiptFileName)
	body, err := localstore.ReadPrivateFile(path)
	if err != nil {
		return installReceipt{}, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var receipt installReceipt
	if err := decoder.Decode(&receipt); err != nil {
		return installReceipt{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return installReceipt{}, fmt.Errorf("decode %s: %w", path, err)
	}
	if receipt.SchemaVersion != installReceiptVersion || receipt.InstallMethod != installReceiptMethod {
		return installReceipt{}, fmt.Errorf("%s is not an Omnara-managed install receipt", path)
	}
	if err := validateReleaseManifestURL(receipt.ReleaseManifestURL); err != nil {
		return installReceipt{}, fmt.Errorf("install receipt release_manifest_url: %w", err)
	}
	return receipt, nil
}

func validateReleaseManifestURL(raw string) error {
	parsed, err := validateReleaseURL(raw)
	if err != nil {
		return err
	}
	manifestPath := parsed.EscapedPath()
	if parsed.ForceQuery || parsed.RawQuery != "" ||
		!(strings.HasSuffix(manifestPath, "/darwin-arm64.txt") ||
			strings.HasSuffix(manifestPath, "/darwin-amd64.txt") ||
			strings.HasSuffix(manifestPath, "/linux-arm64.txt") ||
			strings.HasSuffix(manifestPath, "/linux-amd64.txt")) {
		return errors.New("must be a concrete supported platform manifest")
	}
	return nil
}
