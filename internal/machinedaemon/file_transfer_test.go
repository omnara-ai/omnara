package machinedaemon

import (
	"context"
	"encoding/json"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/stretchr/testify/require"
)

const fileTransferTestProcessID = "prc_aaaaaaaaaaaaaaaaaaaaaaaaae"

func runFileTransferProbe() int {
	if len(os.Args) != 6 || os.Args[2] != "--" || os.Args[3] != "upload" ||
		os.Args[4] != fileTransferTestProcessID || os.Args[5] != "a file;$(false).md" {
		return 2
	}
	result := os.NewFile(3, "file-transfer-result")
	defer func() { _ = result.Close() }()
	_, _ = os.Stdout.WriteString("stdout warning\n")
	_, _ = os.Stderr.WriteString("stderr warning\n")
	_, _ = io.WriteString(result, os.Getenv("FILE_TRANSFER_TEST_RESULT"))
	if os.Getenv("FILE_TRANSFER_TEST_WAIT") == "1" {
		_, _ = io.Copy(io.Discard, os.Stdin)
	}
	code, _ := strconv.Atoi(os.Getenv("FILE_TRANSFER_TEST_EXIT_CODE"))
	return code
}

func TestDetachedSupervisorFileTransfer(t *testing.T) {
	metadata := `{"path":"/memory/team/note.md","digest":"sha256:` + strings.Repeat("a", 64) + `"}`
	failure := `{"error":{"code":"file_content_conflict","error":"file changed","current_digest":"sha256:` +
		strings.Repeat("b", 64) + `"}}`
	for _, tc := range []struct {
		name, result string
		exitCode     int
		wantMetadata bool
		wait         bool
		wantState    daemonprotocol.ProcessState
		wantReason   string
	}{
		{name: "warnings", result: metadata, wantMetadata: true, wantState: daemonprotocol.ProcessStateExited},
		{name: "missing metadata", wantState: daemonprotocol.ProcessStateFailed, wantReason: "invalid_file_transfer_result"},
		{
			name: "invalid metadata", result: "not json",
			wantState: daemonprotocol.ProcessStateFailed, wantReason: "invalid_file_transfer_result",
		},
		{
			name: "missing path", result: `{"digest":"sha256:` + strings.Repeat("a", 64) + `"}`,
			wantMetadata: true, wantState: daemonprotocol.ProcessStateExited,
		},
		{
			name: "missing digest", result: `{"path":"/artifacts/art_test"}`,
			wantMetadata: true, wantState: daemonprotocol.ProcessStateExited,
		},
		{
			name: "invalid digest", result: `{"path":"/artifacts/art_test","digest":"invalid"}`,
			wantMetadata: true, wantState: daemonprotocol.ProcessStateExited,
		},
		{
			name: "oversized metadata", result: strings.Repeat("x", maxFileTransferResultBytes+1),
			wantState: daemonprotocol.ProcessStateFailed, wantReason: "invalid_file_transfer_result",
		},
		{name: "api failure", result: failure, exitCode: 1, wantMetadata: true,
			wantState: daemonprotocol.ProcessStateFailed, wantReason: "nonzero_exit"},
		{name: "local failure", exitCode: 1, wantState: daemonprotocol.ProcessStateFailed, wantReason: "nonzero_exit"},
		{name: "malformed result on failure", result: "not json", exitCode: 1,
			wantState: daemonprotocol.ProcessStateFailed, wantReason: "nonzero_exit"},
		{name: "invalid error", result: `{"error":{}}`, exitCode: 1,
			wantMetadata: true, wantState: daemonprotocol.ProcessStateFailed, wantReason: "nonzero_exit"},
		{name: "success metadata on failure", result: metadata, exitCode: 1,
			wantMetadata: true, wantState: daemonprotocol.ProcessStateFailed, wantReason: "nonzero_exit"},
		{name: "failure metadata on success", result: failure,
			wantMetadata: true, wantState: daemonprotocol.ProcessStateExited},
		{name: "timeout after metadata", result: failure, wait: true,
			wantState: daemonprotocol.ProcessStateFailed, wantReason: "timeout"},
		{name: "timeout", wait: true, wantState: daemonprotocol.ProcessStateFailed, wantReason: "timeout"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			env := map[string]string{
				"FILE_TRANSFER_TEST_RESULT": tc.result, "FILE_TRANSFER_TEST_EXIT_CODE": strconv.Itoa(tc.exitCode),
			}
			timeoutSeconds := 10
			if tc.wait {
				env["FILE_TRANSFER_TEST_WAIT"] = "1"
				timeoutSeconds = 1
			}
			fixture := newDetachedSupervisorTestFixture(t, ctx, ProcessAssignment{
				ID: fileTransferTestProcessID,
				Process: Process{
					FileTransfer: &processcmd.FileTransfer{
						Direction: "upload", LocalPath: "a file;$(false).md",
					},
					IOMode: processcmd.IOModePipe, Cwd: t.TempDir(),
				},
				Env: env, WaitMs: 20, TimeoutSeconds: timeoutSeconds,
			})
			fixture.acceptAndStart(t, ctx)
			fixture.waitClosed(t, 10*time.Second)
			report, event := fixture.terminalEvent(t)
			require.Equal(t, tc.wantState, event.State)
			require.Equal(t, tc.wantReason, event.StateReasonCode)
			var result struct {
				Output       string                             `json:"output"`
				FileTransfer *daemonprotocol.FileTransferResult `json:"file_transfer"`
			}
			require.NoError(t, json.Unmarshal(event.Result, &result))
			require.Contains(t, result.Output, "stdout warning")
			require.Contains(t, result.Output, "stderr warning")
			if tc.wantMetadata {
				require.NotNil(t, result.FileTransfer)
				var want daemonprotocol.FileTransferResult
				require.NoError(t, json.Unmarshal([]byte(tc.result), &want))
				require.Equal(t, want, *result.FileTransfer)
				fixture.client.closeState()
				reopened, err := fixture.client.stateStore(ctx)
				require.NoError(t, err)
				replayed, found, err := reopened.ReportBySlot(ctx, report.Kind, report.ProcessID, "")
				require.NoError(t, err)
				require.True(t, found)
				require.Equal(t, report.Body, replayed.Body)
			} else {
				require.Nil(t, result.FileTransfer)
			}
		})
	}
}

func TestReadFileTransferResultEscapedPath(t *testing.T) {
	component := strings.Repeat("&", 255)
	want := daemonprotocol.FileTransferResult{
		Path:   "/memory/team/" + strings.Join([]string{component, component, component, component}, "/"),
		Digest: "sha256:" + strings.Repeat("a", 64),
	}
	body, err := json.Marshal(want)
	require.NoError(t, err)
	result := readFileTransferResult(strings.NewReader(string(body) + "\n"))
	require.NoError(t, result.err)
	require.Equal(t, &want, result.result)
}

func TestFileTransferDoesNotFallBackToShell(t *testing.T) {
	transfer := &processcmd.FileTransfer{
		Direction: "upload", LocalPath: "file",
	}
	_, err := processArgvForLocalOS(fileTransferTestProcessID, Process{
		FileTransfer: transfer, Command: "touch unexpected", IOMode: processcmd.IOModePipe,
	})
	require.Error(t, err)
	transfer.Direction = "invalid"
	_, err = processArgvForLocalOS(fileTransferTestProcessID, Process{
		FileTransfer: transfer, IOMode: processcmd.IOModePipe,
	})
	require.Error(t, err)
	_, err = processArgvForLocalOS(fileTransferTestProcessID, Process{IOMode: processcmd.IOModePipe})
	require.Error(t, err)
}
