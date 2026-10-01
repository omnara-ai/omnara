package omnarad

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/textutil"
)

const maxFileTransferErrorBytes = 1024

func runFileTransfer(ctx context.Context, direction, processID, localPath string, resultWriter io.Writer) error {
	result, err := transferFile(ctx, direction, processID, localPath)
	if err != nil {
		failure := daemonprotocol.FileTransferError{Code: "file_transfer_failed", Message: err.Error()}
		var apiErr *daemonprotocol.FileTransferError
		if errors.As(err, &apiErr) {
			failure = *apiErr
		}
		failure.Message = textutil.TruncateBytes(failure.Message, maxFileTransferErrorBytes)
		result = daemonprotocol.FileTransferResult{Error: &failure}
	}
	if writeErr := json.NewEncoder(resultWriter).Encode(result); writeErr != nil {
		return errors.Join(err, fmt.Errorf("write file transfer result: %w", writeErr))
	}
	return err
}

func transferFile(
	ctx context.Context, direction, processID, localPath string,
) (daemonprotocol.FileTransferResult, error) {
	transfer := processcmd.FileTransfer{Direction: direction, LocalPath: localPath}
	if err := transfer.Validate(); err != nil {
		return daemonprotocol.FileTransferResult{}, err
	}
	path, err := processcmd.ExpandHomeRelativePath(localPath)
	if err != nil {
		return daemonprotocol.FileTransferResult{}, fmt.Errorf("resolve user home: %w", err)
	}
	if direction == "upload" {
		return uploadFile(ctx, processID, path)
	}
	return downloadFile(ctx, processID, path)
}

func uploadFile(ctx context.Context, processID, path string) (daemonprotocol.FileTransferResult, error) {
	file, err := openTransferFile(path)
	if err != nil {
		return daemonprotocol.FileTransferResult{}, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return daemonprotocol.FileTransferResult{}, fmt.Errorf("inspect file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return daemonprotocol.FileTransferResult{}, errors.New("source must be a regular file")
	}
	if info.Size() > daemonprotocol.MaxFileTransferBytes {
		return daemonprotocol.FileTransferResult{}, errors.New("file exceeds the upload size limit")
	}
	var body io.Reader
	if info.Size() > 0 {
		body = io.NewSectionReader(file, 0, info.Size())
	}
	query := "?filename=" + url.QueryEscape(filepath.Base(path))
	response, err := requestFileTransfer(ctx, processID, query, http.MethodPost, body, info.Size())
	if err != nil {
		return daemonprotocol.FileTransferResult{}, err
	}
	defer func() { _ = response.Body.Close() }()
	bodyBytes, err := readFileTransferResponse(response.Body)
	if err != nil {
		return daemonprotocol.FileTransferResult{}, err
	}
	var result daemonprotocol.FileTransferResult
	if err := json.Unmarshal(bodyBytes, &result); err != nil {
		return result, fmt.Errorf("decode file upload response: %w", err)
	}
	return result, nil
}

func downloadFile(ctx context.Context, processID, path string) (daemonprotocol.FileTransferResult, error) {
	temporary, err := os.CreateTemp(filepath.Dir(path), ".omnara-file-*")
	if err != nil {
		return daemonprotocol.FileTransferResult{}, fmt.Errorf("create temporary file: %w", err)
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	response, err := requestFileTransfer(ctx, processID, "", http.MethodGet, nil, 0)
	if err != nil {
		return daemonprotocol.FileTransferResult{}, err
	}
	defer func() { _ = response.Body.Close() }()
	digest := response.Header.Get("X-Omnara-File-Digest")
	if err := daemonprotocol.ValidateFileDigest(digest); err != nil {
		return daemonprotocol.FileTransferResult{}, errors.New("file transfer response contains an invalid digest")
	}
	if err := writeDownloadedFile(path, temporary, response.Body, digest); err != nil {
		return daemonprotocol.FileTransferResult{}, err
	}
	return daemonprotocol.FileTransferResult{Digest: digest}, nil
}

func requestFileTransfer(
	ctx context.Context, processID, query, method string, body io.Reader, size int64,
) (*http.Response, error) {
	config, _, _, err := loadRuntimeConfig(false)
	if err != nil {
		return nil, fmt.Errorf("load daemon config: %w", err)
	}
	endpoint := strings.TrimRight(config.APIURL, "/") + "/daemon/processes/" +
		url.PathEscape(processID) + "/file" + query
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return nil, fmt.Errorf("create file transfer request: %w", err)
	}
	request.ContentLength = size
	request.Header.Set("Authorization", "Bearer "+config.MachineToken)
	request.Header.Set("Accept", "application/octet-stream")
	if method == http.MethodPost {
		request.Header.Set("Content-Type", "application/octet-stream")
		request.Header.Set("Accept", "application/json")
	}
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	response, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("transfer file: %w", err)
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		defer func() { _ = response.Body.Close() }()
		raw, err := readFileTransferResponse(response.Body)
		if err != nil {
			return nil, err
		}
		var apiErr daemonprotocol.FileTransferError
		if json.Unmarshal(raw, &apiErr) == nil && apiErr.Code != "" && apiErr.Message != "" {
			return nil, fmt.Errorf("transfer file: %w", &apiErr)
		}
		message := strings.TrimSpace(string(raw))
		if message == "" {
			message = response.Status
		}
		return nil, fmt.Errorf("transfer file: %s", message)
	}
	return response, nil
}

func readFileTransferResponse(body io.Reader) ([]byte, error) {
	raw, err := io.ReadAll(io.LimitReader(body, daemonprotocol.MaxMessageBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read file transfer response: %w", err)
	}
	if len(raw) > daemonprotocol.MaxMessageBytes {
		return nil, errors.New("file transfer response is too large")
	}
	return raw, nil
}

func writeDownloadedFile(path string, temporary *os.File, body io.Reader, digest string) error {
	info, err := os.Stat(path)
	if err == nil && info.Mode().IsRegular() {
		if err := temporary.Chmod(info.Mode().Perm()); err != nil {
			return fmt.Errorf("preserve destination permissions: %w", err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination: %w", err)
	}
	const limit = daemonprotocol.MaxFileDownloadBytes
	hash := sha256.New()
	written, err := io.Copy(io.MultiWriter(temporary, hash), io.LimitReader(body, limit+1))
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	if written > limit {
		return errors.New("file download exceeds the size limit")
	}
	if fmt.Sprintf("sha256:%x", hash.Sum(nil)) != digest {
		return errors.New("file download digest mismatch")
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("replace destination: %w", err)
	}
	return nil
}
