package machinedaemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

const maxFileTransferResultBytes = 8 * 1024

type fileTransferOutcome struct {
	result any
	err    error
}

func fileProcessArgv(subcommand string, args ...string) ([]string, error) {
	executable, err := os.Executable()
	if err != nil {
		return nil, fmt.Errorf("resolve file process executable: %w", err)
	}
	return append([]string{executable, subcommand, "--"}, args...), nil
}

func readFileTransferResult(reader io.Reader) fileTransferOutcome {
	var result daemonprotocol.FileTransferResult
	return readFileResult(reader, maxFileTransferResultBytes, &result)
}

func readFileOperationResult(reader io.Reader) fileTransferOutcome {
	var result daemonprotocol.FileOperationResult
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
