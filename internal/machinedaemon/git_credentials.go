package machinedaemon

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localstore"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const gitCredentialCapabilityEnv = "OMNARA_GIT_CREDENTIAL_CAPABILITY"

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
	helper, err := c.processServiceHelper(gitCredentialService)
	if err != nil {
		assignment.PreparationError = "Git credential helper is unavailable"
		return
	}
	helper.Capability = gitCredentialService.capability(supervisorToken, assignment.ID)
	assignment.GitCredentialHelper = helper
}

func (c *Client) startGitCredentialServer(ctx context.Context) (func(), error) {
	requests := make(chan struct{}, 32)
	return c.serveProcessService(ctx, gitCredentialService, &http.Server{
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
	})
}

func (c *Client) serveGitCredentials(w http.ResponseWriter, r *http.Request) {
	processID, ok := strings.CutPrefix(r.URL.Path, "/git-credentials/")
	if _, err := publicid.Decode(publicid.KindProcess, processID); !ok || err != nil || r.Method != http.MethodPost {
		http.NotFound(w, r)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()
	switch c.authorizeProcessService(ctx, gitCredentialService, r, processID) {
	case 0:
	case http.StatusForbidden:
		http.Error(w, "credential request denied", http.StatusForbidden)
		return
	default:
		http.Error(w, "credential service unavailable", http.StatusServiceUnavailable)
		return
	}
	var credentials daemonprotocol.GitCredentials
	if err := c.postJSON(ctx, daemonAPIPath+"/processes/"+processID+"/git-credentials", nil, &credentials); err != nil {
		var status httpStatusError
		if errors.As(err, &status) && status.StatusCode >= 400 && status.StatusCode < 500 {
			if status.RetryAfter != "" {
				w.Header().Set("Retry-After", status.RetryAfter)
			}
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

func gitCredentialEnvironment(env []string, processID string, helper *processServiceHelper) []string {
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

func gitHubCLIEnvironment(env []string, dir, processID string, helper *processServiceHelper) ([]string, error) {
	if err := localstore.EnsurePrivateDir(dir); err != nil {
		return nil, err
	}
	script := "#!/bin/sh\nexec " + gitShellQuote(helper.Executable) + " __omnara_gh " + gitShellQuote(helper.Endpoint) +
		" " + gitShellQuote(processID) + " " + gitShellQuote(dir) + " -- \"$@\"\n"
	if err := os.WriteFile(filepath.Join(dir, "gh"), []byte(script), 0o700); err != nil {
		return nil, err
	}
	result := make([]string, 0, len(env)+1)
	path := dir
	for _, entry := range env {
		if value, ok := strings.CutPrefix(entry, "PATH="); ok {
			if value != "" {
				path += string(os.PathListSeparator) + value
			}
			continue
		}
		result = append(result, entry)
	}
	return append(result, "PATH="+path), nil
}

func gitShellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
