package omnarad

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
	"unicode"

	"github.com/omnara-ai/omnara/internal/daemonversion"
	"github.com/omnara-ai/omnara/internal/machinedaemon"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
)

const (
	updatePollInterval         = 5 * time.Minute
	updateManifestMaxBytes     = 64 * 1024
	updateArtifactMaxBytes     = 256 * 1024 * 1024
	updateDownloadTimeout      = 60 * time.Second
	updateConnectTimeout       = 10 * time.Second
	updateVersionTimeout       = 10 * time.Second
	updateRuntimeShutdownLimit = 30 * time.Second
	updateFailureReportTimeout = 5 * time.Second
	updateStagingDirName       = ".omnarad-update"
	inheritedDaemonLockFDEnv   = "__OMNARA_DAEMON_LOCK_FD"
)

type updateDiscoveryStatus uint8

const (
	updateSkipped updateDiscoveryStatus = iota
	updateUnchanged
	updateAvailable
)

type releaseManifest struct {
	Version string
	URL     string
	SHA256  string
}

type daemonUpdatePolicy struct {
	receipt        installReceipt
	currentVersion daemonversion.Release
}

type daemonUpdate struct {
	policy   daemonUpdatePolicy
	manifest releaseManifest
}

type stagedDaemonUpdate struct {
	update daemonUpdate
	dir    string
	path   string
}

type updateFailureReporter struct {
	client        *machinedaemon.Client
	log           *slog.Logger
	daemonVersion string
	mu            sync.Mutex
	lastReported  string
}

func (r *updateFailureReporter) report(ctx context.Context, step, targetVersion string, cause error) {
	if r == nil || ctx.Err() != nil {
		return
	}
	detail := step
	if cause != nil {
		detail += ": " + cause.Error()
	}
	key := targetVersion + "\x00" + detail
	r.mu.Lock()
	if r.lastReported == key {
		r.mu.Unlock()
		return
	}
	r.lastReported = key
	r.mu.Unlock()
	reportCtx, cancel := context.WithTimeout(ctx, updateFailureReportTimeout)
	defer cancel()
	if err := r.client.ReportUpdateFailure(reportCtx, machinedaemon.UpdateFailureReport{
		DaemonVersion: r.daemonVersion,
		TargetVersion: targetVersion,
		Detail:        detail,
	}); err != nil {
		r.log.Warn("report daemon update failure failed", "error", err)
		r.mu.Lock()
		if r.lastReported == key {
			r.lastReported = ""
		}
		r.mu.Unlock()
	}
}

func runDaemonService(
	ctx context.Context,
	clientConfig machinedaemon.Config,
	noUpdate bool,
	executable string,
	supervised bool,
	log *slog.Logger,
) error {
	var daemonLock *localstore.Lock
	if !supervised {
		var err error
		daemonLock, err = acquireDaemonRuntimeLock(clientConfig.OmnaraHome)
		if err != nil {
			return err
		}
		if err := daemonLock.WritePID(os.Getpid()); err != nil {
			_ = daemonLock.Release()
			return err
		}
		defer func() {
			if err := daemonLock.Release(); err != nil {
				log.Warn("release daemon local lock failed", "error", err)
			}
		}()
	}

	runCtx, cancelRun := context.WithCancelCause(ctx)
	defer cancelRun(nil)
	client := machinedaemon.New(clientConfig, nil, log)
	reporter := &updateFailureReporter{client: &client, log: log, daemonVersion: version}
	reexecArgs := []string{runServiceSubcommand}
	if supervised {
		reexecArgs = append(reexecArgs, supervisedServiceFlag)
	}
	clientDone := make(chan error, 1)
	go func() {
		err := client.Run(runCtx)
		cancelRun(err)
		clientDone <- err
	}()

	updates := make(chan daemonUpdate, 1)
	startPoller := func() {
		go pollDaemonUpdates(runCtx, clientConfig.OmnaraHome, executable, version, updates, reporter, log)
	}
	if version != daemonversion.Development && !noUpdate {
		startPoller()
	}

	for {
		select {
		case err := <-clientDone:
			if ctx.Err() != nil && errors.Is(err, context.Canceled) {
				return nil
			}
			return err
		case update := <-updates:
			staged, err := stageDaemonRelease(runCtx, clientConfig.OmnaraHome, update, updateStagingDirName)
			if err != nil {
				if runCtx.Err() == nil {
					log.Warn("daemon update staging failed", "version", update.manifest.Version, "error", err)
					reporter.report(runCtx, "staging", update.manifest.Version, err)
					startPoller()
				}
				continue
			}
			cleanup := func() {
				if err := os.RemoveAll(staged.dir); err != nil {
					log.Warn("remove staged daemon update failed", "path", staged.dir, "error", err)
				}
			}
			lock, acquired, err := tryAcquireInstallLock(clientConfig.OmnaraHome)
			if err != nil || !acquired {
				cleanup()
				if err != nil {
					log.Warn("daemon update lock failed", "error", err)
					reporter.report(runCtx, "install lock", update.manifest.Version, err)
				}
				startPoller()
				continue
			}
			release := func() {
				cleanup()
				if err := lock.Release(); err != nil {
					log.Warn("daemon update lock release failed", "error", err)
				}
			}
			valid, err := revalidateDaemonUpdate(runCtx, clientConfig.OmnaraHome, executable, version, staged)
			if err != nil || !valid {
				release()
				if err != nil {
					log.Warn("daemon update revalidation failed", "version", update.manifest.Version, "error", err)
					reporter.report(runCtx, "revalidation", update.manifest.Version, err)
				}
				startPoller()
				continue
			}

			cancelRun(machinedaemon.ErrDaemonUpdate)
			if !errors.Is(context.Cause(runCtx), machinedaemon.ErrDaemonUpdate) {
				release()
				err := <-clientDone
				if ctx.Err() != nil && errors.Is(err, context.Canceled) {
					return nil
				}
				return err
			}

			timer := time.NewTimer(updateRuntimeShutdownLimit)
			var clientErr error
			shutdownTimedOut := false
			select {
			case clientErr = <-clientDone:
				timer.Stop()
			case <-ctx.Done():
				timer.Stop()
				release()
				return nil
			case <-timer.C:
				shutdownTimedOut = true
			}
			canonical := canonicalDaemonPath(clientConfig.OmnaraHome)
			if shutdownTimedOut || (clientErr != nil && !errors.Is(clientErr, context.Canceled)) {
				release()
				if shutdownTimedOut {
					log.Warn("daemon update shutdown timed out", "version", update.manifest.Version)
					reporter.report(ctx, "runtime shutdown timeout", update.manifest.Version, nil)
				} else {
					log.Warn("daemon update shutdown failed", "version", update.manifest.Version, "error", clientErr)
					reporter.report(ctx, "runtime shutdown", update.manifest.Version, clientErr)
				}
				return reexecUpdatedDaemon(ctx, canonical, daemonLock, reexecArgs...)
			}

			if ctx.Err() != nil {
				release()
				return nil
			}
			renameErr := os.Rename(staged.path, canonical)
			release()
			if renameErr != nil {
				log.Warn("daemon update replacement failed", "version", update.manifest.Version, "error", renameErr)
				reporter.report(ctx, "binary replacement", update.manifest.Version, renameErr)
			}
			return reexecUpdatedDaemon(ctx, canonical, daemonLock, reexecArgs...)
		}
	}
}

func acquireDaemonRuntimeLock(home string) (*localstore.Lock, error) {
	store, err := localstore.New(home)
	if err != nil {
		return nil, err
	}
	path := store.DaemonLockPath()
	if value, ok := os.LookupEnv(inheritedDaemonLockFDEnv); ok {
		if err := os.Unsetenv(inheritedDaemonLockFDEnv); err != nil {
			return nil, fmt.Errorf("clear inherited daemon lock: %w", err)
		}
		fd, err := strconv.Atoi(value)
		if err != nil {
			return nil, errors.New("invalid inherited daemon lock file descriptor")
		}
		lock, err := localstore.AdoptLock(path, fd)
		if err != nil {
			return nil, fmt.Errorf("adopt inherited daemon lock: %w", err)
		}
		return lock, nil
	}
	lock, err := localstore.TryAcquireLock(path)
	if errors.Is(err, localstore.ErrLockHeld) {
		return nil, errors.New("another daemon is already running in OMNARA_HOME")
	}
	if err != nil {
		return nil, err
	}
	return lock, nil
}

func pollDaemonUpdates(
	ctx context.Context,
	home string,
	executable string,
	currentVersion string,
	updates chan<- daemonUpdate,
	reporter *updateFailureReporter,
	log *slog.Logger,
) {
	initial := true
	for {
		delay := updatePollInterval - updatePollInterval/5 + time.Duration(
			rand.Int64N(int64(2*updatePollInterval/5)),
		)
		if initial {
			delay = time.Duration(rand.Int64N(int64(updatePollInterval)))
			initial = false
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		status, update, err := discoverDaemonUpdate(ctx, home, executable, currentVersion)
		if err != nil {
			if ctx.Err() == nil {
				log.Warn("daemon update discovery failed", "error", err)
				reporter.report(ctx, "discovery", "", err)
			}
			continue
		}
		switch status {
		case updateSkipped:
			return
		case updateUnchanged:
			continue
		case updateAvailable:
			select {
			case updates <- update:
			case <-ctx.Done():
			}
			return
		}
	}
}

func discoverDaemonUpdate(
	ctx context.Context,
	home string,
	executable string,
	currentVersion string,
) (updateDiscoveryStatus, daemonUpdate, error) {
	policy, enabled, err := loadDaemonUpdatePolicy(home, executable, currentVersion)
	if err != nil {
		return updateSkipped, daemonUpdate{}, err
	}
	if !enabled {
		return updateSkipped, daemonUpdate{}, nil
	}
	var body bytes.Buffer
	if err := downloadPublicRelease(ctx, policy.receipt.ReleaseManifestURL, updateManifestMaxBytes, &body); err != nil {
		return updateSkipped, daemonUpdate{}, fmt.Errorf("download release manifest: %w", err)
	}
	manifest, err := parseReleaseManifest(body.Bytes())
	if err != nil {
		return updateSkipped, daemonUpdate{}, err
	}
	availableVersion, err := daemonversion.ParseRelease(manifest.Version)
	if err != nil {
		return updateSkipped, daemonUpdate{}, err
	}
	if daemonversion.Compare(availableVersion, policy.currentVersion) <= 0 {
		return updateUnchanged, daemonUpdate{}, nil
	}
	return updateAvailable, daemonUpdate{policy: policy, manifest: manifest}, nil
}

func loadDaemonUpdatePolicy(home, executable, currentVersion string) (daemonUpdatePolicy, bool, error) {
	if currentVersion == daemonversion.Development {
		return daemonUpdatePolicy{}, false, nil
	}
	receipt, err := loadInstallReceipt(home)
	if errors.Is(err, os.ErrNotExist) {
		return daemonUpdatePolicy{}, false, nil
	}
	if err != nil {
		return daemonUpdatePolicy{}, false, err
	}
	matches, err := executableMatchesCanonical(executable, canonicalDaemonPath(home))
	if err != nil {
		return daemonUpdatePolicy{}, false, err
	}
	if !matches {
		return daemonUpdatePolicy{}, false, nil
	}
	parsedVersion, err := daemonversion.ParseRelease(currentVersion)
	if err != nil {
		return daemonUpdatePolicy{}, false, fmt.Errorf("parse running daemon version: %w", err)
	}
	return daemonUpdatePolicy{receipt: receipt, currentVersion: parsedVersion}, true, nil
}

func validateReleaseURL(raw string) (*url.URL, error) {
	if raw == "" || strings.TrimSpace(raw) != raw || strings.ContainsRune(raw, '\\') {
		return nil, errors.New("release URL is invalid")
	}
	for _, r := range raw {
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return nil, errors.New("release URL is invalid")
		}
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Hostname() == "" || parsed.User != nil || parsed.Fragment != "" {
		return nil, errors.New("release URL is invalid")
	}
	parsed.Scheme = strings.ToLower(parsed.Scheme)
	switch parsed.Scheme {
	case "https":
	case "http":
		if !isLoopbackHost(strings.ToLower(parsed.Hostname())) {
			return nil, errors.New("release URL must use https outside loopback development")
		}
	default:
		return nil, errors.New("release URL must use http or https")
	}
	return parsed, nil
}

func parseReleaseManifest(body []byte) (releaseManifest, error) {
	manifest := releaseManifest{}
	counts := map[string]int{}
	for _, line := range strings.Split(string(body), "\n") {
		key, value, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch key {
		case "version":
			counts[key]++
			manifest.Version = value
		case "url":
			counts[key]++
			manifest.URL = value
		case "sha256":
			counts[key]++
			manifest.SHA256 = strings.ToLower(value)
		}
	}
	if counts["version"] != 1 || manifest.Version == "" {
		return releaseManifest{}, errors.New("release manifest must contain exactly one version")
	}
	if counts["url"] != 1 || manifest.URL == "" {
		return releaseManifest{}, errors.New("release manifest must contain exactly one url")
	}
	if counts["sha256"] != 1 || len(manifest.SHA256) != 64 {
		return releaseManifest{}, errors.New("release manifest must contain exactly one valid sha256")
	}
	if _, err := daemonversion.ParseRelease(manifest.Version); err != nil {
		return releaseManifest{}, fmt.Errorf("release manifest version: %w", err)
	}
	if _, err := validateReleaseURL(manifest.URL); err != nil {
		return releaseManifest{}, fmt.Errorf("release manifest url: %w", err)
	}
	for _, r := range manifest.SHA256 {
		if (r < '0' || r > '9') && (r < 'a' || r > 'f') {
			return releaseManifest{}, errors.New("release manifest sha256 is invalid")
		}
	}
	return manifest, nil
}

func downloadPublicRelease(ctx context.Context, rawURL string, maxBytes int64, destination io.Writer) error {
	parsed, err := validateReleaseURL(rawURL)
	if err != nil {
		return err
	}
	defaultTransport, ok := http.DefaultTransport.(*http.Transport)
	if !ok {
		return errors.New("default HTTP transport is not configurable")
	}
	transport := defaultTransport.Clone()
	transport.DialContext = (&net.Dialer{Timeout: updateConnectTimeout, KeepAlive: 30 * time.Second}).DialContext
	client := &http.Client{
		Transport: transport,
		Timeout:   updateDownloadTimeout,
		CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if len(via) > 5 {
				return errors.New("too many release redirects")
			}
			redirect, err := validateReleaseURL(request.URL.String())
			if err != nil {
				return err
			}
			if parsed.Scheme != "https" || redirect.Scheme != "https" {
				return errors.New("release redirect must remain https")
			}
			return nil
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return err
	}
	response, err := client.Do(request)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("release server returned HTTP %d", response.StatusCode)
	}
	if response.ContentLength > maxBytes {
		return errors.New("release download exceeds size limit")
	}
	written, err := io.Copy(destination, io.LimitReader(response.Body, maxBytes+1))
	if err != nil {
		return err
	}
	if written > maxBytes {
		return errors.New("release download exceeds size limit")
	}
	return nil
}

func stageDaemonRelease(
	ctx context.Context,
	home string,
	update daemonUpdate,
	stagingDirName string,
) (*stagedDaemonUpdate, error) {
	binDir := filepath.Dir(canonicalDaemonPath(home))
	info, err := os.Lstat(binDir)
	if err != nil {
		return nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return nil, fmt.Errorf("daemon bin path must be a directory: %s", binDir)
	}
	dir := filepath.Join(binDir, stagingDirName)
	if err := os.RemoveAll(dir); err != nil {
		return nil, fmt.Errorf("remove stale daemon update stage: %w", err)
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return nil, fmt.Errorf("create daemon update stage: %w", err)
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(dir)
		}
	}()
	path := filepath.Join(dir, "omnarad")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, err
	}
	downloadErr := downloadPublicRelease(ctx, update.manifest.URL, updateArtifactMaxBytes, file)
	syncErr := file.Sync()
	closeErr := file.Close()
	if err := errors.Join(downloadErr, syncErr, closeErr); err != nil {
		return nil, err
	}
	staged := &stagedDaemonUpdate{update: update, dir: dir, path: path}
	if err := verifyStagedDaemonChecksum(staged); err != nil {
		return nil, err
	}
	if err := os.Chmod(path, 0o700); err != nil {
		return nil, err
	}
	if err := verifyStagedDaemonVersion(ctx, staged); err != nil {
		return nil, err
	}
	cleanup = false
	return staged, nil
}

func verifyStagedDaemonUpdate(ctx context.Context, staged *stagedDaemonUpdate) error {
	if err := verifyStagedDaemonChecksum(staged); err != nil {
		return err
	}
	return verifyStagedDaemonVersion(ctx, staged)
}

func verifyStagedDaemonChecksum(staged *stagedDaemonUpdate) error {
	file, err := os.Open(staged.path)
	if err != nil {
		return err
	}
	hash := sha256.New()
	_, hashErr := io.Copy(hash, file)
	closeErr := file.Close()
	if err := errors.Join(hashErr, closeErr); err != nil {
		return err
	}
	if fmt.Sprintf("%x", hash.Sum(nil)) != staged.update.manifest.SHA256 {
		return errors.New("downloaded omnarad checksum mismatch")
	}
	return nil
}

func verifyStagedDaemonVersion(ctx context.Context, staged *stagedDaemonUpdate) error {
	stagedVersion, err := readExecutableVersion(ctx, staged.path)
	if err != nil {
		return fmt.Errorf("validate downloaded omnarad version: %w", err)
	}
	if stagedVersion != staged.update.manifest.Version {
		return errors.New("downloaded omnarad version does not match release manifest")
	}
	return nil
}

func revalidateDaemonUpdate(
	ctx context.Context,
	home string,
	executable string,
	currentVersion string,
	staged *stagedDaemonUpdate,
) (bool, error) {
	policy, enabled, err := loadDaemonUpdatePolicy(home, executable, currentVersion)
	if err != nil {
		return false, err
	}
	if !enabled || policy.receipt != staged.update.policy.receipt {
		return false, nil
	}
	canonicalVersion, err := readExecutableVersion(ctx, canonicalDaemonPath(home))
	if err != nil {
		return false, err
	}
	if canonicalVersion != currentVersion {
		return false, nil
	}
	if err := verifyStagedDaemonUpdate(ctx, staged); err != nil {
		return false, err
	}
	return true, nil
}

func readExecutableVersion(ctx context.Context, path string) (string, error) {
	commandCtx, cancel := context.WithTimeout(ctx, updateVersionTimeout)
	defer cancel()
	cmd := exec.CommandContext(commandCtx, path, versionFlag)
	cmd.Env = executableVersionEnv()
	output, err := cmd.Output()
	if err != nil {
		return "", err
	}
	value := strings.TrimSuffix(string(output), "\n")
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return "", errors.New("omnarad --version returned invalid output")
	}
	if _, err := daemonversion.ParseRelease(value); err != nil {
		return "", err
	}
	return value, nil
}

func executableVersionEnv() []string {
	keys := []string{"HOME", "PATH", "TMPDIR", "LANG", "LC_ALL", "LC_CTYPE"}
	env := make([]string, 0, len(keys))
	for _, key := range keys {
		if value, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+value)
		}
	}
	return env
}

func executableMatchesCanonical(executable, canonical string) (bool, error) {
	info, err := os.Lstat(canonical)
	if err != nil {
		return false, err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return false, nil
	}
	resolvedExecutable, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return false, err
	}
	resolvedCanonical, err := filepath.EvalSymlinks(canonical)
	if err != nil {
		return false, err
	}
	return filepath.Clean(resolvedExecutable) == filepath.Clean(resolvedCanonical), nil
}

func canonicalDaemonPath(home string) string {
	return filepath.Join(home, "bin", "omnarad")
}

func reexecDaemon(ctx context.Context, path string, args ...string) error {
	return execDaemon(ctx, path, os.Environ(), args...)
}

func reexecUpdatedDaemon(
	ctx context.Context,
	path string,
	daemonLock *localstore.Lock,
	args ...string,
) error {
	if daemonLock == nil {
		return reexecDaemon(ctx, path, args...)
	}
	fd, restore, err := daemonLock.PrepareForExec()
	if err != nil {
		return err
	}
	defer restore()
	env := append(os.Environ(), inheritedDaemonLockFDEnv+"="+strconv.Itoa(fd))
	return execDaemon(ctx, path, env, args...)
}

func execDaemon(ctx context.Context, path string, env []string, args ...string) error {
	select {
	case <-ctx.Done():
		return nil
	default:
	}
	if err := syscall.Exec(path, append([]string{path}, args...), env); err != nil {
		return fmt.Errorf("reexec omnarad: %w", err)
	}
	return nil
}
