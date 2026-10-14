package machinedaemon

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
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
	if _, err := publicid.Decode(publicid.KindProcess, processID); err != nil {
		return errors.New("invalid credential process")
	}
	capability := os.Getenv(gitCredentialCapabilityEnv)
	if endpoint == "" || capability == "" {
		return errors.New("git credential helper is unavailable outside its agent process")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			for {
				conn, err := localipc.Dial(connectCtx, endpoint)
				if err == nil {
					return conn, nil
				}
				if err := sleepContext(connectCtx, 250*time.Millisecond); err != nil {
					return nil, errors.New("daemon is unavailable for Git credentials")
				}
			}
		},
	}
	defer transport.CloseIdleConnections()
	client := http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://daemon/git-credentials/"+processID, nil)
	if err != nil {
		return errors.New("cannot prepare Git credential request")
	}
	request.Header.Set("Authorization", "Bearer "+capability)
	var response *http.Response
	for attempt := 0; ; attempt++ {
		response, err = client.Do(request)
		if (err == nil && response.StatusCode != http.StatusServiceUnavailable) || attempt == 2 || ctx.Err() != nil {
			break
		}
		if response != nil {
			_ = response.Body.Close()
		}
		if err := sleepContext(ctx, time.Duration(attempt+1)*250*time.Millisecond); err != nil {
			return errors.New("git credential request timed out")
		}
	}
	if err != nil {
		return errors.New("daemon could not provide Git credentials")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusUnauthorized || response.StatusCode == http.StatusForbidden ||
		response.StatusCode == http.StatusNotFound {
		return errors.New("git credentials are not authorized for this process")
	}
	if response.StatusCode != http.StatusOK {
		return errors.New("git credentials unavailable; check integration, Contents permission, and machine settings")
	}
	var credentials daemonprotocol.GitCredentials
	decoder := json.NewDecoder(io.LimitReader(response.Body, 65536))
	if err := decoder.Decode(&credentials); err != nil || credentials.Token == "" ||
		strings.ContainsAny(credentials.Token, "\x00\r\n") || !credentials.ExpiresAt.After(time.Now()) {
		return errors.New("invalid Git credential response")
	}
	_, err = fmt.Fprintf(output, "username=x-access-token\npassword=%s\npassword_expiry_utc=%d\n\n",
		credentials.Token, credentials.ExpiresAt.Unix())
	return err
}
