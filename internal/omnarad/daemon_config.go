package omnarad

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"golang.org/x/term"
)

const (
	daemonConfigFileName = "daemon.json"
	daemonConfigVersion  = 1
	defaultAPIURL        = "https://api.omnara.com/v1"
	legacyHostedAPIURL   = "https://app.omnara.com"
	missingTokenError    = "OMNARA_MACHINE_TOKEN is required; " +
		"rerun from an interactive terminal or set OMNARA_MACHINE_TOKEN"
)

type daemonConfig struct {
	SchemaVersion  int    `json:"schema_version"`
	APIURL         string `json:"api_url"`
	InstallationID string `json:"installation_id"`
	MachineID      string `json:"machine_id"`
	MachineToken   string `json:"machine_token"`
	NoUpdate       bool   `json:"no_update"`
	RunnerPath     string `json:"runner_path"`
}

type daemonConfigDocument struct {
	SchemaVersion  *int    `json:"schema_version"`
	APIURL         *string `json:"api_url"`
	InstallationID *string `json:"installation_id"`
	MachineID      *string `json:"machine_id"`
	MachineToken   *string `json:"machine_token"`
	NoUpdate       *bool   `json:"no_update"`
	RunnerPath     *string `json:"runner_path"`
}

func writeDaemonConfig(
	ctx context.Context,
	stdin *os.File,
	stderr io.Writer,
	log *slog.Logger,
) error {
	home, err := localstore.ResolveHome()
	if err != nil {
		return err
	}
	existing, err := loadDaemonConfig(home)
	hasConfig := err == nil
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	hasState := false
	if !hasConfig {
		hasState, err = localstore.HasDurableMachineState(home)
		if err != nil {
			return err
		}
		if hasState && strings.TrimSpace(os.Getenv("OMNARA_API_URL")) == "" {
			return fmt.Errorf(
				"OMNARA_API_URL is required to recover the existing daemon installation because %s is missing",
				filepath.Join(home, daemonConfigFileName),
			)
		}
	}

	config := daemonConfig{
		SchemaVersion: daemonConfigVersion,
		APIURL:        defaultAPIURL,
		RunnerPath:    os.Getenv("PATH"),
	}
	if hasConfig {
		config = *existing
	}
	if _, err := applyDaemonEnvironment(&config); err != nil {
		return err
	}
	if config.MachineToken == "" {
		config.MachineToken, err = promptMachineToken(stdin, stderr)
		if err != nil {
			return err
		}
	}

	clientConfig := machinedaemon.Config{
		APIURL:                 config.APIURL,
		MachineToken:           config.MachineToken,
		OmnaraHome:             home,
		ExpectedInstallationID: config.InstallationID,
		ExpectedMachineID:      config.MachineID,
	}
	identity, err := validateDaemonBinding(ctx, clientConfig, log)
	if err != nil {
		return err
	}
	if !hasConfig && hasState {
		machine, err := localstore.Machine(home, identity.InstallationID, identity.MachineID)
		if err != nil {
			return err
		}
		info, err := os.Lstat(machine.MachineDir())
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if errors.Is(err, os.ErrNotExist) || !info.IsDir() {
			return fmt.Errorf(
				"%s is missing and authenticated machine does not match existing durable state",
				filepath.Join(home, daemonConfigFileName),
			)
		}
	}
	config.InstallationID = identity.InstallationID
	config.MachineID = identity.MachineID
	if hasConfig && *existing == config {
		return nil
	}
	if err := localstore.WriteJSONAtomic(filepath.Join(home, daemonConfigFileName), config, 0o600); err != nil {
		return err
	}
	return nil
}

func applyDaemonEnvironment(config *daemonConfig) (bool, error) {
	before := *config
	if rawAPIURL := strings.TrimSpace(os.Getenv("OMNARA_API_URL")); rawAPIURL != "" {
		apiURL, err := canonicalAPIURL(rawAPIURL)
		if err != nil {
			return false, fmt.Errorf("OMNARA_API_URL: %w", err)
		}
		config.APIURL = migrateLegacyAPIURL(apiURL)
	}
	if token := os.Getenv("OMNARA_MACHINE_TOKEN"); token != "" {
		config.MachineToken = token
	}
	if value, ok := os.LookupEnv("OMNARA_NO_UPDATE"); ok {
		switch value {
		case "0":
			config.NoUpdate = false
		case "1":
			config.NoUpdate = true
		default:
			return false, errors.New("OMNARA_NO_UPDATE must be 0 or 1")
		}
	}
	if value := os.Getenv("OMNARA_RUNNER_PATH"); value != "" {
		config.RunnerPath = value
	}
	return before != *config, nil
}

func validateDaemonBinding(
	ctx context.Context,
	config machinedaemon.Config,
	log *slog.Logger,
) (machinedaemon.BootstrapIdentity, error) {
	client := machinedaemon.New(config, nil, log)
	identity, err := client.ValidateBootstrap(ctx)
	if err != nil {
		if machinedaemon.IsAuthenticationRejected(err) {
			return machinedaemon.BootstrapIdentity{}, fmt.Errorf("OMNARA_MACHINE_TOKEN was rejected by %s", config.APIURL)
		}
		return machinedaemon.BootstrapIdentity{}, fmt.Errorf(
			"validate OMNARA_MACHINE_TOKEN with %s: %w", config.APIURL, err,
		)
	}
	if _, err := localstore.Machine(config.OmnaraHome, identity.InstallationID, identity.MachineID); err != nil {
		return machinedaemon.BootstrapIdentity{}, fmt.Errorf("validate daemon binding: %w", err)
	}
	return identity, nil
}

func loadDaemonConfig(home string) (*daemonConfig, error) {
	path := filepath.Join(home, daemonConfigFileName)
	body, err := localstore.ReadPrivateFile(path)
	if err != nil {
		return nil, err
	}
	decoder := json.NewDecoder(bytes.NewReader(body))
	decoder.DisallowUnknownFields()
	var document daemonConfigDocument
	if err := decoder.Decode(&document); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return nil, fmt.Errorf("decode %s: %w", path, err)
	}
	if document.SchemaVersion == nil {
		return nil, fmt.Errorf("%s is missing required fields", path)
	}
	if *document.SchemaVersion != daemonConfigVersion {
		return nil, fmt.Errorf("unsupported daemon config schema_version %d", *document.SchemaVersion)
	}
	if document.APIURL == nil || document.InstallationID == nil || document.MachineID == nil ||
		document.MachineToken == nil || document.NoUpdate == nil || document.RunnerPath == nil {
		return nil, fmt.Errorf("%s is missing required fields", path)
	}
	apiURL, err := canonicalAPIURL(*document.APIURL)
	if err != nil {
		return nil, fmt.Errorf("daemon config api_url: %w", err)
	}
	if apiURL != *document.APIURL {
		return nil, errors.New("daemon config api_url is not canonical")
	}
	apiURL = migrateLegacyAPIURL(apiURL)
	if *document.MachineToken == "" {
		return nil, errors.New("daemon config machine_token is required")
	}
	if _, err := localstore.Machine(home, *document.InstallationID, *document.MachineID); err != nil {
		return nil, fmt.Errorf("daemon config binding: %w", err)
	}
	return &daemonConfig{
		SchemaVersion:  *document.SchemaVersion,
		APIURL:         apiURL,
		InstallationID: *document.InstallationID,
		MachineID:      *document.MachineID,
		MachineToken:   *document.MachineToken,
		NoUpdate:       *document.NoUpdate,
		RunnerPath:     *document.RunnerPath,
	}, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	return nil
}

func promptMachineToken(stdin *os.File, stderr io.Writer) (string, error) {
	if stdin != nil && term.IsTerminal(int(stdin.Fd())) {
		return readMachineToken(stdin, stderr)
	}
	tty, err := os.OpenFile("/dev/tty", os.O_RDWR, 0)
	if err != nil {
		return "", errors.New(missingTokenError)
	}
	defer func() { _ = tty.Close() }()
	if !term.IsTerminal(int(tty.Fd())) {
		return "", errors.New(missingTokenError)
	}
	return readMachineToken(tty, tty)
}

func readMachineToken(terminal *os.File, output io.Writer) (string, error) {
	_, _ = fmt.Fprint(output, "Enter Omnara machine token: ")
	value, err := term.ReadPassword(int(terminal.Fd()))
	_, _ = fmt.Fprintln(output)
	if err != nil {
		return "", fmt.Errorf("read Omnara machine token: %w", err)
	}
	token := strings.TrimSpace(string(value))
	if token == "" {
		return "", errors.New(missingTokenError)
	}
	return token, nil
}

func migrateLegacyAPIURL(apiURL string) string {
	if apiURL == legacyHostedAPIURL {
		return defaultAPIURL
	}
	parsed, err := url.Parse(apiURL)
	if err != nil || parsed.Path != "" {
		return apiURL
	}
	return apiURL + "/api/v1"
}

func canonicalAPIURL(raw string) (string, error) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", fmt.Errorf("invalid API URL: %w", err)
	}
	if parsed.Opaque != "" || parsed.Host == "" {
		return "", errors.New("API URL must be absolute")
	}
	if parsed.User != nil {
		return "", errors.New("API URL must not contain user info")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" {
		return "", errors.New("API URL must not contain a query or fragment")
	}
	scheme := strings.ToLower(parsed.Scheme)
	if scheme != "http" && scheme != "https" {
		return "", errors.New("API URL scheme must be https or loopback http")
	}
	hostname := strings.ToLower(parsed.Hostname())
	if hostname == "" {
		return "", errors.New("API URL host is required")
	}
	if scheme == "http" && hostname != "host.docker.internal" && !isLoopbackHost(hostname) {
		return "", errors.New("API URL must use https unless the host is local")
	}
	port := parsed.Port()
	if port != "" {
		portNumber, err := strconv.Atoi(port)
		if err != nil || portNumber < 1 || portNumber > 65535 {
			return "", errors.New("API URL port is invalid")
		}
	}
	if (scheme == "https" && port == "443") || (scheme == "http" && port == "80") {
		port = ""
	}
	if port != "" {
		parsed.Host = net.JoinHostPort(hostname, port)
	} else if strings.Contains(hostname, ":") {
		parsed.Host = "[" + hostname + "]"
	} else {
		parsed.Host = hostname
	}
	parsed.Scheme = scheme
	escapedPath := strings.TrimRight(parsed.EscapedPath(), "/")
	parsed.Path, err = url.PathUnescape(escapedPath)
	if err != nil {
		return "", fmt.Errorf("invalid API URL path: %w", err)
	}
	parsed.RawPath = escapedPath
	return parsed.String(), nil
}

func isLoopbackHost(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func loadRuntimeConfig(applyEnvironment bool) (machinedaemon.Config, bool, bool, error) {
	home, err := localstore.ResolveHome()
	if err != nil {
		return machinedaemon.Config{}, false, false, err
	}
	config, err := loadDaemonConfig(home)
	if errors.Is(err, os.ErrNotExist) {
		return machinedaemon.Config{}, false, false, fmt.Errorf(
			"%s is required; run 'omnarad install' to create it",
			filepath.Join(home, daemonConfigFileName),
		)
	}
	if err != nil {
		return machinedaemon.Config{}, false, false, err
	}
	overridden := false
	if applyEnvironment {
		overridden, err = applyDaemonEnvironment(config)
		if err != nil {
			return machinedaemon.Config{}, false, false, err
		}
	}
	runtimeConfig := machinedaemon.Config{
		APIURL:                 config.APIURL,
		MachineToken:           config.MachineToken,
		DaemonVersion:          version,
		OmnaraHome:             home,
		ExpectedInstallationID: config.InstallationID,
		ExpectedMachineID:      config.MachineID,
		RunnerPath:             config.RunnerPath,
	}
	return runtimeConfig, config.NoUpdate, overridden, nil
}

func validateRuntimeConfig(
	ctx context.Context,
	log *slog.Logger,
) (string, bool, error) {
	config, _, overridden, err := loadRuntimeConfig(true)
	if err != nil {
		return "", false, err
	}
	if _, err := validateDaemonBinding(ctx, config, log); err != nil {
		return "", false, err
	}
	return config.OmnaraHome, overridden, nil
}

func runService(ctx context.Context, log *slog.Logger, supervised bool) error {
	runtimeConfig, noUpdate, _, err := loadRuntimeConfig(supervised)
	if err != nil {
		return err
	}
	if err := applyRuntimeEnvironment(&runtimeConfig); err != nil {
		return err
	}
	executable, err := os.Executable()
	if err != nil {
		return err
	}
	return runDaemonService(ctx, runtimeConfig, noUpdate, executable, supervised, log)
}

func applyRuntimeEnvironment(runtimeConfig *machinedaemon.Config) error {
	if value := os.Getenv("OMNARA_DAEMON_RETRY_INTERVAL_MS"); value != "" {
		milliseconds, err := strconv.Atoi(value)
		if err != nil || milliseconds <= 0 {
			return errors.New("OMNARA_DAEMON_RETRY_INTERVAL_MS must be positive integer milliseconds")
		}
		runtimeConfig.RetryInterval = time.Duration(milliseconds) * time.Millisecond
	}
	if value := os.Getenv(daemonprotocol.SleepAfterEnvVar); value != "" {
		milliseconds, err := strconv.Atoi(value)
		if err != nil {
			return fmt.Errorf("%s must be integer milliseconds", daemonprotocol.SleepAfterEnvVar)
		}
		sleepAfter, err := daemonprotocol.SleepAfterDuration(milliseconds)
		if err != nil {
			return fmt.Errorf("%s %w", daemonprotocol.SleepAfterEnvVar, err)
		}
		runtimeConfig.SleepAfter = sleepAfter
	}
	runtimeConfig.WakeListenAddr = os.Getenv(daemonprotocol.WakeListenAddrEnvVar)
	if runtimeConfig.WakeListenAddr == "" && runtimeConfig.SleepAfter > 0 {
		runtimeConfig.WakeListenAddr = ":" + strconv.Itoa(daemonprotocol.WakeListenerPort)
	}
	runtimeConfig.SleepPlatform = os.Getenv(daemonprotocol.SleepPlatformEnvVar)
	return nil
}
