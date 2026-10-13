//go:build integration && servicee2e

package e2e

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/testutil"
	"github.com/stretchr/testify/require"
)

// TestServiceE2EDockerMachineOperations drives the agent-free machine exec
// and file routes against a real daemon container.
func TestServiceE2EDockerMachineOperations(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	env := newServiceE2EEnvironment(t, ctx, "docker-machine-operations")
	env.startAPI(t, ctx)
	project := env.bootstrapProjectViaAPI(t, ctx, "machine-operations", "openai-prod", "service-e2e-local")
	machine := project.bootstrapDockerMachine(t, ctx, "machine-operations")
	env.startDaemonContainer(t, ctx, machine.daemonToken, machine.workdir)
	waitForDaemonRuntime(t, ctx, env, project.orgID, machine.machineID)
	waitForDockerMachineOnline(t, ctx, env, project.orgID, machine.machineID)
	machinePath := "/api/v1/orgs/" + project.orgID + "/machines/" + machine.machineID

	exec := env.requestJSON(t, ctx, http.MethodPost, machinePath+"/exec", map[string]any{
		"command": "echo out; echo err >&2; echo \"$GREETING\"; pwd; exit 7",
		"cwd":     "/work",
		"env":     map[string]string{"GREETING": "hello"},
	}, "", project.adminToken, http.StatusOK)
	require.Equal(t, float64(7), exec["exit_code"])
	require.Equal(t, "out\nerr\nhello\n/work\n", exec["output"])
	require.Equal(t, false, exec["timed_out"])
	require.Equal(t, false, exec["output_truncated"])

	timedOut := env.requestJSON(t, ctx, http.MethodPost, machinePath+"/exec", map[string]any{
		"command":         "echo started; sleep 60",
		"timeout_seconds": 1,
	}, "", project.adminToken, http.StatusOK)
	require.Equal(t, true, timedOut["timed_out"])
	require.Nil(t, timedOut["exit_code"])
	require.Equal(t, "started\n", timedOut["output"])

	content := []byte("binary\x00payload\nline two\n")
	remote := "/work/uploads/nested/data.bin"
	upload := machineFileRequest(t, ctx, env, project.adminToken, http.MethodPut, machinePath, remote, content)
	require.Equal(t, http.StatusOK, upload.status, string(upload.body))

	check := env.requestJSON(t, ctx, http.MethodPost, machinePath+"/exec", map[string]any{
		"command": "wc -c < " + remote,
	}, "", project.adminToken, http.StatusOK)
	require.Equal(t, float64(0), check["exit_code"])
	require.Contains(t, testutil.RequireType[string](t, check["output"]), "24")

	download := machineFileRequest(t, ctx, env, project.adminToken, http.MethodGet, machinePath, remote, nil)
	require.Equal(t, http.StatusOK, download.status, string(download.body))
	require.Equal(t, content, download.body)

	missing := machineFileRequest(t, ctx, env, project.adminToken, http.MethodGet, machinePath, "/work/missing", nil)
	require.Equal(t, http.StatusNotFound, missing.status, string(missing.body))
}

type machineFileResponse struct {
	status int
	body   []byte
}

func machineFileRequest(
	t *testing.T,
	ctx context.Context,
	env *serviceE2EEnvironment,
	token, method, machinePath, remotePath string,
	body []byte,
) machineFileResponse {
	t.Helper()
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := env.newAPIRequest(ctx, method, machinePath+"/files?path="+url.QueryEscape(remotePath), reader)
	require.NoError(t, err)
	req.Header.Set("Authorization", "Bearer "+token)
	if body != nil {
		req.Header.Set("Content-Type", "application/octet-stream")
	}
	resp, err := http.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return machineFileResponse{status: resp.StatusCode, body: data}
}
