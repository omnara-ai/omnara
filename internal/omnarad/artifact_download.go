package omnarad

import (
	"context"
	"encoding/base64"
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

func runDownloadArtifactCommand(
	ctx context.Context,
	toolCallID string,
	artifactID string,
	encodedPath string,
) error {
	if _, err := publicid.Decode(publicid.KindToolCall, toolCallID); err != nil {
		return errors.New("invalid tool call id")
	}
	if _, err := publicid.Decode(publicid.KindArtifact, artifactID); err != nil {
		return errors.New("invalid artifact id")
	}
	rawPath, err := base64.RawURLEncoding.DecodeString(encodedPath)
	if err != nil {
		return fmt.Errorf("decode artifact path: %w", err)
	}
	if len(rawPath) == 0 {
		return errors.New("artifact path is required")
	}
	if strings.Contains(string(rawPath), "\x00") {
		return errors.New("artifact path cannot contain NUL")
	}
	path, err := processcmd.ExpandHomeRelativePath(string(rawPath))
	if err != nil {
		return fmt.Errorf("resolve user home: %w", err)
	}
	config, _, _, err := loadRuntimeConfig(false)
	if err != nil {
		return fmt.Errorf("load daemon config: %w", err)
	}
	endpoint := strings.TrimRight(config.APIURL, "/") +
		"/daemon/tool-calls/" + url.PathEscape(toolCallID) +
		"/artifacts/" + url.PathEscape(artifactID) + "/content"
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return fmt.Errorf("create artifact download request: %w", err)
	}
	request.Header.Set("Authorization", "Bearer "+config.MachineToken)
	request.Header.Set("Accept", "application/octet-stream")
	client := &http.Client{
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
	directory := filepath.Dir(path)
	temporary, err := os.CreateTemp(directory, ".omnara-artifact-*")
	if err != nil {
		return fmt.Errorf("create temporary artifact file: %w", err)
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	info, statErr := os.Stat(path)
	if statErr == nil && info.Mode().IsRegular() {
		if err := temporary.Chmod(info.Mode().Perm()); err != nil {
			return fmt.Errorf("preserve artifact destination permissions: %w", err)
		}
	} else if statErr != nil && !errors.Is(statErr, os.ErrNotExist) {
		return fmt.Errorf("inspect artifact destination: %w", statErr)
	}
	response, err := client.Do(request)
	if err != nil {
		return fmt.Errorf("download artifact: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		rawResponse, readErr := io.ReadAll(io.LimitReader(response.Body, daemonprotocol.MaxMessageBytes+1))
		if readErr != nil {
			return fmt.Errorf("read artifact download response: %w", readErr)
		}
		if len(rawResponse) > daemonprotocol.MaxMessageBytes {
			return errors.New("artifact download response is too large")
		}
		message := strings.TrimSpace(string(rawResponse))
		if message == "" {
			message = response.Status
		}
		return fmt.Errorf("download artifact: %s", message)
	}
	written, err := io.Copy(
		temporary,
		io.LimitReader(response.Body, daemonprotocol.MaxArtifactUploadBytes+1),
	)
	if err != nil {
		return fmt.Errorf("write artifact: %w", err)
	}
	if written > daemonprotocol.MaxArtifactUploadBytes {
		return errors.New("artifact download exceeds the size limit")
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close artifact: %w", err)
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return fmt.Errorf("replace artifact destination: %w", err)
	}
	return nil
}
