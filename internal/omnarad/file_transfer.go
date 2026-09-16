package omnarad

import (
	"bytes"
	"context"
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

type fileTransferRequest struct {
	direction      string
	toolCallID     string
	encodedPath    string
	endpointSuffix string
	requireDigest  bool
}

type fileTransferResult struct {
	Path   string `json:"path,omitempty"`
	Digest string `json:"digest,omitempty"`
}

func runFileTransfer(ctx context.Context, transfer fileTransferRequest, stdout io.Writer) error {
	if transfer.direction != "upload" && transfer.direction != "download" {
		return errors.New("invalid transfer direction")
	}
	if _, err := publicid.Decode(publicid.KindToolCall, transfer.toolCallID); err != nil {
		return errors.New("invalid tool call id")
	}
	rawPath, err := base64.RawURLEncoding.DecodeString(transfer.encodedPath)
	if err != nil {
		return fmt.Errorf("decode file path: %w", err)
	}
	if len(rawPath) == 0 || bytes.ContainsRune(rawPath, 0) {
		return errors.New("file path must be non-empty and cannot contain NUL")
	}
	path, err := processcmd.ExpandHomeRelativePath(string(rawPath))
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	var temporary *os.File
	var body io.Reader
	var size int64
	method := http.MethodGet
	if transfer.direction == "upload" {
		file, err := openTransferFile(path)
		if err != nil {
			return fmt.Errorf("open file: %w", err)
		}
		defer func() { _ = file.Close() }()
		info, err := file.Stat()
		if err != nil {
			return fmt.Errorf("inspect file: %w", err)
		}
		if !info.Mode().IsRegular() {
			return errors.New("source must be a regular file")
		}
		if info.Size() == 0 && !transfer.requireDigest {
			return errors.New("artifact file cannot be empty")
		}
		if info.Size() > daemonprotocol.MaxFileTransferBytes {
			return errors.New("file exceeds the upload size limit")
		}
		size = info.Size()
		if size > 0 {
			body = io.NewSectionReader(file, 0, size)
		}
		method = http.MethodPost
	} else {
		temporary, err = os.CreateTemp(filepath.Dir(path), ".omnara-file-*")
		if err != nil {
			return fmt.Errorf("create temporary file: %w", err)
		}
		defer func() {
			_ = temporary.Close()
			_ = os.Remove(temporary.Name())
		}()
	}
	config, _, _, err := loadRuntimeConfig(false)
	if err != nil {
		return fmt.Errorf("load daemon config: %w", err)
	}
	endpoint := strings.TrimRight(config.APIURL, "/") + "/daemon/tool-calls/" +
		url.PathEscape(transfer.toolCallID) + transfer.endpointSuffix
	if method == http.MethodPost {
		endpoint += "?filename=" + url.QueryEscape(filepath.Base(path))
	}
	request, err := http.NewRequestWithContext(ctx, method, endpoint, body)
	if err != nil {
		return fmt.Errorf("create file transfer request: %w", err)
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
		return fmt.Errorf("transfer file: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		raw, err := readFileTransferResponse(response.Body)
		if err != nil {
			return err
		}
		message := strings.TrimSpace(string(raw))
		if message == "" {
			message = response.Status
		}
		return fmt.Errorf("transfer file: %s", message)
	}
	var result fileTransferResult
	if method == http.MethodPost {
		raw, err := readFileTransferResponse(response.Body)
		if err != nil {
			return err
		}
		if transfer.endpointSuffix == "/artifact" {
			return writeArtifactUploadResult(raw, stdout)
		}
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
	} else {
		result.Digest = response.Header.Get("X-Omnara-File-Digest")
	}
	if method == http.MethodPost || transfer.requireDigest || result.Digest != "" {
		if err := daemonprotocol.ValidateFileDigest(result.Digest); err != nil {
			return errors.New("file transfer response contains an invalid digest")
		}
	}
	if method == http.MethodGet {
		if err := writeDownloadedFile(path, temporary, response.Body); err != nil {
			return err
		}
		if result.Digest == "" {
			return nil
		}
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return fmt.Errorf("write file transfer result: %w", err)
	}
	return nil
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

func writeDownloadedFile(path string, temporary *os.File, body io.Reader) error {
	info, err := os.Stat(path)
	if err == nil && info.Mode().IsRegular() {
		if err := temporary.Chmod(info.Mode().Perm()); err != nil {
			return fmt.Errorf("preserve destination permissions: %w", err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect destination: %w", err)
	}
	const limit = daemonprotocol.MaxFileDownloadBytes
	written, err := io.Copy(temporary, io.LimitReader(body, limit+1))
	if err != nil {
		return fmt.Errorf("write file: %w", err)
	}
	if written > limit {
		return errors.New("file download exceeds the size limit")
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close file: %w", err)
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return fmt.Errorf("replace destination: %w", err)
	}
	return nil
}
