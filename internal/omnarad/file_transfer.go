package omnarad

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
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
	"github.com/omnara-ai/omnara/internal/publicid"
)

type fileTransferResult struct {
	Path   string `json:"path,omitempty"`
	Digest string `json:"digest,omitempty"`
}

func runFileTransfer(ctx context.Context, direction, toolCallID, encodedPath string, stdout io.Writer) error {
	switch direction {
	case "download":
		return downloadFile(ctx, toolCallID, encodedPath, "/file", true, stdout)
	case "upload":
		raw, err := uploadFile(ctx, toolCallID, encodedPath, "/file", true)
		if err != nil {
			return err
		}
		var result fileTransferResult
		decoder := json.NewDecoder(bytes.NewReader(raw))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&result); err != nil {
			return fmt.Errorf("decode file upload response: %w", err)
		}
		if err := requireJSONEOF(decoder); err != nil {
			return fmt.Errorf("decode file upload response: %w", err)
		}
		if result.Path == "" {
			return errors.New("file upload response is missing path")
		}
		if err := daemonprotocol.ValidateFileDigest(result.Digest); err != nil {
			return errors.New("file transfer response contains an invalid digest")
		}
		if err := json.NewEncoder(stdout).Encode(result); err != nil {
			return fmt.Errorf("write file transfer result: %w", err)
		}
		return nil
	default:
		return errors.New("invalid transfer direction")
	}
}

func uploadFile(ctx context.Context, toolCallID, encodedPath, endpointSuffix string, allowEmpty bool) ([]byte, error) {
	path, err := resolveTransferPath(toolCallID, encodedPath)
	if err != nil {
		return nil, err
	}
	file, err := openTransferFile(path)
	if err != nil {
		return nil, fmt.Errorf("open file: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect file: %w", err)
	}
	if !info.Mode().IsRegular() {
		return nil, errors.New("source must be a regular file")
	}
	if info.Size() == 0 && !allowEmpty {
		return nil, errors.New("file cannot be empty")
	}
	if info.Size() > daemonprotocol.MaxFileTransferBytes {
		return nil, errors.New("file exceeds the upload size limit")
	}
	var body io.Reader
	if info.Size() > 0 {
		body = io.NewSectionReader(file, 0, info.Size())
	}
	endpointSuffix += "?filename=" + url.QueryEscape(filepath.Base(path))
	response, err := requestFileTransfer(ctx, toolCallID, endpointSuffix, http.MethodPost, body, info.Size())
	if err != nil {
		return nil, err
	}
	defer func() { _ = response.Body.Close() }()
	return readFileTransferResponse(response.Body)
}

func downloadFile(
	ctx context.Context, toolCallID, encodedPath, endpointSuffix string, requireDigest bool, stdout io.Writer,
) error {
	path, err := resolveTransferPath(toolCallID, encodedPath)
	if err != nil {
		return err
	}
	temporary, err := os.CreateTemp(filepath.Dir(path), ".omnara-file-*")
	if err != nil {
		return fmt.Errorf("create temporary file: %w", err)
	}
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporary.Name())
	}()
	response, err := requestFileTransfer(ctx, toolCallID, endpointSuffix, http.MethodGet, nil, 0)
	if err != nil {
		return err
	}
	defer func() { _ = response.Body.Close() }()
	digest := response.Header.Get("X-Omnara-File-Digest")
	if requireDigest || digest != "" {
		if err := daemonprotocol.ValidateFileDigest(digest); err != nil {
			return errors.New("file transfer response contains an invalid digest")
		}
	}
	if err := writeDownloadedFile(path, temporary, response.Body, digest); err != nil {
		return err
	}
	if digest == "" {
		return nil
	}
	if err := json.NewEncoder(stdout).Encode(fileTransferResult{Digest: digest}); err != nil {
		return fmt.Errorf("write file transfer result: %w", err)
	}
	return nil
}

func resolveTransferPath(toolCallID, encodedPath string) (string, error) {
	if _, err := publicid.Decode(publicid.KindToolCall, toolCallID); err != nil {
		return "", errors.New("invalid tool call id")
	}
	rawPath, err := base64.RawURLEncoding.DecodeString(encodedPath)
	if err != nil {
		return "", fmt.Errorf("decode file path: %w", err)
	}
	if len(rawPath) == 0 || bytes.ContainsRune(rawPath, 0) {
		return "", errors.New("file path must be non-empty and cannot contain NUL")
	}
	path, err := processcmd.ExpandHomeRelativePath(string(rawPath))
	if err != nil {
		return "", fmt.Errorf("resolve user home: %w", err)
	}
	return path, nil
}

func requestFileTransfer(
	ctx context.Context, toolCallID, endpointSuffix, method string, body io.Reader, size int64,
) (*http.Response, error) {
	config, _, _, err := loadRuntimeConfig(false)
	if err != nil {
		return nil, fmt.Errorf("load daemon config: %w", err)
	}
	endpoint := strings.TrimRight(config.APIURL, "/") + "/daemon/tool-calls/" +
		url.PathEscape(toolCallID) + endpointSuffix
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
	if digest != "" && fmt.Sprintf("sha256:%x", hash.Sum(nil)) != digest {
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
