package machinedaemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func RunGitCredentialHelper(
	ctx context.Context,
	endpoint, processID, operation string,
	input io.Reader,
	output io.Writer,
) (resultErr error) {
	if operation == "store" || operation == "erase" {
		return nil
	}
	defer func() {
		if resultErr != nil {
			_, _ = io.WriteString(output, "quit=true\n\n")
		}
	}()
	if operation != "get" {
		return errors.New("unsupported Git credential operation")
	}
	var protocol, host string
	scanner := bufio.NewScanner(io.LimitReader(input, 65537))
	size := 0
	for scanner.Scan() {
		line := scanner.Text()
		size += len(line) + 1
		if size > 65536 {
			return errors.New("git credential request is too large")
		}
		if line == "" {
			break
		}
		key, value, _ := strings.Cut(line, "=")
		switch key {
		case "protocol":
			protocol = value
		case "host":
			host = value
		}
	}
	if scanner.Err() != nil {
		return errors.New("invalid Git credential request")
	}
	if protocol != "https" || (!strings.EqualFold(host, "github.com") && !strings.EqualFold(host, "github.com:443")) {
		return errors.New("git credentials support only https://github.com")
	}
	credentials, err := requestGitCredentials(ctx, endpoint, processID)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(output, "username=x-access-token\npassword=%s\npassword_expiry_utc=%d\n\n",
		credentials.Token, credentials.ExpiresAt.Unix())
	return err
}

var ErrGitHubCLINotFound = errors.New("gh: command not found")

func RunGitHubCLI(ctx context.Context, endpoint, processID, wrapperDir string, args []string) error {
	var path []string
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		if filepath.Clean(dir) != filepath.Clean(wrapperDir) {
			path = append(path, dir)
		}
	}
	if err := os.Setenv("PATH", strings.Join(path, string(os.PathListSeparator))); err != nil {
		return err
	}
	gh, err := exec.LookPath("gh")
	if err != nil {
		return ErrGitHubCLINotFound
	}
	if os.Getenv("GH_TOKEN") == "" && os.Getenv("GITHUB_TOKEN") == "" {
		credentials, err := requestGitCredentials(ctx, endpoint, processID)
		if err != nil {
			return err
		}
		if err := os.Setenv("GH_TOKEN", credentials.Token); err != nil {
			return err
		}
	}
	return syscall.Exec(gh, append([]string{"gh"}, args...), os.Environ())
}

func requestGitCredentials(
	ctx context.Context, endpoint, processID string,
) (daemonprotocol.GitCredentials, error) {
	var credentials daemonprotocol.GitCredentials
	if _, err := publicid.Decode(publicid.KindProcess, processID); err != nil {
		return credentials, errors.New("invalid credential process")
	}
	capability := os.Getenv(gitCredentialCapabilityEnv)
	if endpoint == "" || capability == "" {
		return credentials, errors.New("git credential helper is unavailable outside its agent process")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	transport := dialProcessService(endpoint, "daemon is unavailable for Git credentials")
	defer transport.CloseIdleConnections()
	client := http.Client{Transport: transport, CheckRedirect: noRedirects}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://daemon/git-credentials/"+processID, nil)
	if err != nil {
		return credentials, errors.New("cannot prepare Git credential request")
	}
	request.Header.Set("Authorization", "Bearer "+capability)
	var response *http.Response
	for attempt := 0; ; attempt++ {
		response, err = client.Do(request)
		if (err == nil && response.StatusCode != http.StatusServiceUnavailable &&
			response.StatusCode != http.StatusTooManyRequests) || attempt == 2 || ctx.Err() != nil {
			break
		}
		delay := time.Duration(attempt+1) * 250 * time.Millisecond
		if response != nil {
			if retryAfter, ok := parseRetryAfter(response.Header.Get("Retry-After"), time.Now()); ok {
				delay = max(delay, retryAfter)
			}
			_ = response.Body.Close()
		}
		if err := sleepContext(ctx, delay); err != nil {
			return credentials, errors.New("git credential request timed out")
		}
	}
	if err != nil {
		return credentials, errors.New("daemon could not provide Git credentials")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden ||
		response.StatusCode == http.StatusNotFound {
		return credentials, errors.New("git credentials are not authorized for this process")
	}
	if response.StatusCode == http.StatusTooManyRequests {
		return credentials, errors.New("git credentials are rate limited; try again shortly")
	}
	if response.StatusCode != http.StatusOK {
		return credentials, errors.New(
			"git credentials unavailable; check integration, Contents permission, and machine settings")
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 65536))
	if err := decoder.Decode(&credentials); err != nil || credentials.Token == "" ||
		strings.ContainsAny(credentials.Token, "\x00\r\n") || !credentials.ExpiresAt.After(time.Now()) {
		return daemonprotocol.GitCredentials{}, errors.New("invalid Git credential response")
	}
	return credentials, nil
}
