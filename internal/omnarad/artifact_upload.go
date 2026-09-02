package omnarad

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path/filepath"
	"strings"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/processcmd"
	"github.com/omnara-ai/omnara/internal/publicid"
)

type uploadArtifactResponse struct {
	ArtifactID string `json:"artifact_id"`
}

func runUploadArtifactCommand(
	ctx context.Context,
	toolCallID string,
	encodedPath string,
	stdout io.Writer,
) error {
	if _, err := publicid.Decode(publicid.KindToolCall, toolCallID); err != nil {
		return errors.New("invalid tool call id")
	}
	rawPath, err := base64.RawURLEncoding.DecodeString(encodedPath)
	if err != nil {
		return fmt.Errorf("decode artifact path: %w", err)
	}
	path, err := processcmd.ExpandHomeRelativePath(string(rawPath))
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	file, err := openArtifactFile(path)
	if err != nil {
		return fmt.Errorf("open artifact: %w", err)
	}
	defer func() { _ = file.Close() }()
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect artifact: %w", err)
	}
	if !info.Mode().IsRegular() {
		return errors.New("artifact path must be a regular file")
	}
	if info.Size() == 0 {
		return errors.New("artifact file cannot be empty")
	}
	if info.Size() > daemonprotocol.MaxArtifactUploadBytes {
		return fmt.Errorf("artifact file exceeds %d bytes", daemonprotocol.MaxArtifactUploadBytes)
	}
	config, _, _, err := loadRuntimeConfig(false)
	if err != nil {
		return fmt.Errorf("load daemon config: %w", err)
	}
	endpoint := strings.TrimRight(config.APIURL, "/") +
		"/daemon/tool-calls/" + url.PathEscape(toolCallID) +
		"/artifact?filename=" + url.QueryEscape(filepath.Base(path))
	body := io.NewSectionReader(file, 0, info.Size())
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, body)
	if err != nil {
		return fmt.Errorf("create artifact upload request: %w", err)
	}
	request.ContentLength = info.Size()
	request.Header.Set("Authorization", "Bearer "+config.MachineToken)
	request.Header.Set("Content-Type", "application/octet-stream")
	request.Header.Set("Accept", "application/json")
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("upload artifact: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	rawResponse, err := io.ReadAll(io.LimitReader(response.Body, daemonprotocol.MaxMessageBytes+1))
	if err != nil {
		return fmt.Errorf("read artifact upload response: %w", err)
	}
	if len(rawResponse) > daemonprotocol.MaxMessageBytes {
		return errors.New("artifact upload response is too large")
	}
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		message := strings.TrimSpace(string(rawResponse))
		if message == "" {
			message = response.Status
		}
		return fmt.Errorf("upload artifact: %s", message)
	}
	decoder := json.NewDecoder(strings.NewReader(string(rawResponse)))
	decoder.DisallowUnknownFields()
	var result uploadArtifactResponse
	if err := decoder.Decode(&result); err != nil {
		return fmt.Errorf("decode artifact upload response: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return fmt.Errorf("decode artifact upload response: %w", err)
	}
	if _, err := publicid.Decode(publicid.KindArtifact, result.ArtifactID); err != nil {
		return errors.New("artifact upload response contains an invalid artifact id")
	}
	if err := json.NewEncoder(stdout).Encode(result); err != nil {
		return fmt.Errorf("write artifact upload result: %w", err)
	}
	return nil
}
