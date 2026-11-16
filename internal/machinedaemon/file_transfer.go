package machinedaemon

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"golang.org/x/net/http/httpproxy"
)

const maxFileTransferResultBytes = 8 * 1024

type FileTransferConnection struct {
	APIURL       string `json:"api_url"`
	MachineToken string `json:"machine_token"`
	ProxyURL     string `json:"proxy_url,omitempty"`
	SSLCertFile  string `json:"ssl_cert_file,omitempty"`
	SSLCertDir   string `json:"ssl_cert_dir,omitempty"`
}

func (c *Client) fileTransferConnection() (FileTransferConnection, error) {
	apiURL, err := url.Parse(c.cfg.APIURL)
	if err != nil {
		return FileTransferConnection{}, fmt.Errorf("parse file transfer API URL: %w", err)
	}
	proxyURL, err := httpproxy.FromEnvironment().ProxyFunc()(apiURL)
	if err != nil {
		return FileTransferConnection{}, fmt.Errorf("resolve file transfer proxy: %w", err)
	}
	connection := FileTransferConnection{
		APIURL:       c.cfg.APIURL,
		MachineToken: c.cfg.MachineToken,
		SSLCertFile:  os.Getenv("SSL_CERT_FILE"),
		SSLCertDir:   os.Getenv("SSL_CERT_DIR"),
	}
	if proxyURL != nil {
		connection.ProxyURL = proxyURL.String()
	}
	return connection, nil
}

func fileTransferConnectionPipe(connection *FileTransferConnection) (*os.File, error) {
	if connection == nil {
		return nil, errors.New("file transfer connection is missing")
	}
	reader, writer, err := os.Pipe()
	if err != nil {
		return nil, fmt.Errorf("create file transfer connection pipe: %w", err)
	}
	if err := errors.Join(json.NewEncoder(writer).Encode(connection), writer.Close()); err != nil {
		_ = reader.Close()
		return nil, fmt.Errorf("write file transfer connection: %w", err)
	}
	return reader, nil
}

type fileTransferOutcome struct {
	result *daemonprotocol.FileTransferResult
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
