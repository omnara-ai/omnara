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

// fileTransferOutcome is the structured result a file transfer or file
// operation child writes to its result pipe.
type fileTransferOutcome struct {
	result any
	err    error
}

func fileTransferArgv(processID string, transfer processcmd.FileTransfer) ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve file transfer executable: %w", err)
	}
	return []string{
		executable, "__omnara_file_transfer", "--",
		string(transfer.Direction), processID, transfer.LocalPath,
	}, nil
}

// fileOperationArgv runs a file operation in omnarad, which reads the
// execution spec from stdin.
func fileOperationArgv(processID string) ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve file operation executable: %w", err)
	}
	return []string{executable, "__omnara_file_operation", "--", processID}, nil
}

func readFileTransferResult(reader io.Reader) fileTransferOutcome {
	var result daemonprotocol.FileTransferResult
	return readFileResult(reader, maxFileTransferResultBytes, &result)
}

func readFileOperationResult(reader io.Reader) fileTransferOutcome {
	var result daemonprotocol.FileOperationResult
	// Leave room for the JSON envelope around the bounded value.
	return readFileResult(reader, daemonprotocol.MaxFileOperationResultBytes+16*1024, &result)
}

func readFileResult(reader io.Reader, limit int, result any) fileTransferOutcome {
	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return fileTransferOutcome{err: fmt.Errorf("read file transfer result: %w", err)}
	}
	if len(body) > limit {
		return fileTransferOutcome{err: errors.New("file transfer result exceeds size limit")}
	}
	if err := json.Unmarshal(body, result); err != nil {
		return fileTransferOutcome{err: fmt.Errorf("decode file transfer result: %w", err)}
	}
	return fileTransferOutcome{result: result}
}

func (r *localProcessRunner) captureFileTransferResult(exit *processRunnerExit) any {
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
