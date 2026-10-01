package machinedaemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processcmd"
)

const maxFileTransferResultBytes = 8 * 1024

type fileTransferOutcome struct {
	result *daemonprotocol.FileTransferResult
	err    error
}

func fileTransferArgv(processID string, transfer processcmd.FileTransfer) ([]string, error) {
	if err := transfer.Validate(); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve file transfer executable: %w", err)
	}
	return []string{
		executable, "__omnara_file_transfer", "--",
		transfer.Direction, processID, transfer.LocalPath,
	}, nil
}

func readFileTransferResult(reader io.Reader) fileTransferOutcome {
	body, err := io.ReadAll(io.LimitReader(reader, maxFileTransferResultBytes+1))
	if err != nil {
		return fileTransferOutcome{err: fmt.Errorf("read file transfer result: %w", err)}
	}
	if len(body) > maxFileTransferResultBytes {
		return fileTransferOutcome{err: errors.New("file transfer result exceeds size limit")}
	}
	var result daemonprotocol.FileTransferResult
	if err := json.Unmarshal(body, &result); err != nil {
		return fileTransferOutcome{err: fmt.Errorf("decode file transfer result: %w", err)}
	}
	return fileTransferOutcome{result: &result}
}

func (r *localProcessRunner) captureFileTransferResult(exit *processRunnerExit) *daemonprotocol.FileTransferResult {
	if r.transferResult == nil {
		return nil
	}
	if exit.ExitCode == nil {
		_ = r.transferReader.Close()
		return nil
	}
	outcome := <-r.transferResult
	if outcome.err != nil && exit.State == daemonprotocol.ProcessStateExited && *exit.ExitCode == 0 {
		exit.State = daemonprotocol.ProcessStateFailed
		exit.StateReasonCode = "invalid_file_transfer_result"
		exit.StateReasonMessage = outcome.err.Error()
	}
	return outcome.result
}
