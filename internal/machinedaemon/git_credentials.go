package machinedaemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const gitCredentialCapabilityEnv = "OMNARA_GIT_CREDENTIAL_CAPABILITY"

type gitCredentialHelper struct {
	Executable string `json:"executable"`
	Endpoint   string `json:"endpoint"`
	Capability string `json:"capability"`
}

func gitCredentialCapability(supervisorToken, processID string) string {
	mac := hmac.New(sha256.New, []byte(supervisorToken))
	_, _ = mac.Write([]byte("omnara-git-credential\x00" + processID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *Client) gitCredentialEndpoint() (string, error) {
	machine, err := c.machineStore()
	if err != nil {
		return "", err
	}
	return filepath.Join(machine.RunDir(), "git-credentials.sock"), nil
}

func (c *Client) prepareGitCredentials(assignment *ProcessAssignment, supervisorToken string) {
	if assignment.Process.ExecutionSpec.Kind != processcmd.KindShell {
		assignment.GitCredentials = false
		assignment.GitCredentialHelper = nil
		return
	}
	if !assignment.GitCredentials || assignment.PreparationError != "" {
		return
	}
	if c.cfg.GitCredentialsDisabled {
		assignment.PreparationError = "Git credentials are disabled on this machine"
		return
	}
	if c.gitCredentialsUnavailable {
		assignment.PreparationError = "Git credential service is unavailable on this machine; restart the daemon"
		return
	}
	endpoint, err := c.gitCredentialEndpoint()
	if err != nil {
		assignment.PreparationError = "Git credential endpoint is unavailable"
		return
	}
	executable, err := os.Executable()
	if err != nil {
		assignment.PreparationError = "Git credential helper executable is unavailable"
		return
	}
	assignment.GitCredentialHelper = &gitCredentialHelper{
		Executable: executable,
		Endpoint:   endpoint,
		Capability: gitCredentialCapability(supervisorToken, assignment.ID),
	}
}

func (c *Client) startGitCredentialServer(ctx context.Context) (func(), error) {
	endpoint, err := c.gitCredentialEndpoint()
	if err != nil {
		return nil, err
	}
	if err := localipc.Cleanup(endpoint); err != nil {
		return nil, err
	}
	listener, err := localipc.Listen(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	requests := make(chan struct{}, 32)
	serverCtx, cancel := context.WithCancel(ctx)
	server := &http.Server{
		BaseContext:       func(net.Listener) context.Context { return serverCtx },
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       5 * time.Second,
		WriteTimeout:      35 * time.Second,
		IdleTimeout:       5 * time.Second,
		MaxHeaderBytes:    4096,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Cache-Control", "no-store")
			select {
			case requests <- struct{}{}:
				defer func() { <-requests }()
			default:
				http.Error(w, "credential service busy", http.StatusServiceUnavailable)
				return
			}
			c.serveGitCredentials(w, r)
		}),
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error("Git credential service stopped", "error", err)
		}
	}()
	return func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		<-done
		_ = localipc.Cleanup(endpoint)
	}, nil
}

func (c *Client) serveGitCredentials(w http.ResponseWriter, r *http.Request) {
	processID, ok := strings.CutPrefix(r.URL.Path, "/git-credentials/")
	if _, err := publicid.Decode(publicid.KindProcess, processID); !ok || err != nil || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	state, err := c.stateStore(ctx)
	if err != nil {
		http.Error(w, "credential service unavailable", http.StatusServiceUnavailable)
		return
	}
	process, found, err := state.Process(ctx, processID)
	if err != nil {
		http.Error(w, "credential service unavailable", http.StatusServiceUnavailable)
		return
	}
	capability, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	want := gitCredentialCapability(process.SupervisorToken, processID)
	if !found || process.LocalClosed || !bearer || !hmac.Equal([]byte(capability), []byte(want)) {
		http.Error(w, "credential request denied", http.StatusForbidden)
		return
	}
	var credentials daemonprotocol.GitCredentials
	if err := c.postJSON(ctx, daemonAPIPath+"/processes/"+processID+"/git-credentials", nil, &credentials); err != nil {
		var status httpStatusError
		if errors.As(err, &status) && status.StatusCode >= 400 && status.StatusCode < 500 &&
			status.StatusCode != http.StatusTooManyRequests {
			http.Error(w, "Git credentials are unavailable for this process", status.StatusCode)
		} else {
			http.Error(w, "Git credential service temporarily unavailable", http.StatusServiceUnavailable)
		}
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if err := json.NewEncoder(w).Encode(credentials); err != nil {
		c.log.Debug("Git credential response could not be written")
	}
}

func gitCredentialEnvironment(env []string, processID string, helper *gitCredentialHelper) []string {
	parameters := ""
	result := make([]string, 0, len(env)+2)
	for _, entry := range env {
		name, value, _ := strings.Cut(entry, "=")
		switch {
		case strings.EqualFold(name, "GIT_CONFIG_PARAMETERS"):
			parameters = value
		case strings.EqualFold(name, gitCredentialCapabilityEnv):
		default:
			result = append(result, entry)
		}
	}
	command := "!" + gitShellQuote(filepath.ToSlash(helper.Executable)) + " __omnara_git_credential " +
		gitShellQuote(helper.Endpoint) + " " + gitShellQuote(processID)
	// Git reads PARAMETERS after COUNT: https://github.com/git/git/blob/v2.50.0/config.c#L733-L775
	if parameters != "" {
		parameters += " "
	}
	parameters += gitShellQuote("credential.https://github.com.helper=") +
		" " + gitShellQuote("credential.https://github.com.helper="+command)
	return append(result, "GIT_CONFIG_PARAMETERS="+parameters, gitCredentialCapabilityEnv+"="+helper.Capability)
}

func gitShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
