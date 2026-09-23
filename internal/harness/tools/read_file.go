package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type readFileRequest struct {
	Path       string `json:"path"`
	OffsetLine *int   `json:"offset_line,omitempty"`
	LimitLines *int   `json:"limit_lines,omitempty"`
	OffsetChar *int   `json:"offset_char,omitempty"`
	LimitChars *int   `json:"limit_chars,omitempty"`
}

func validateReadFileInput(raw json.RawMessage) error {
	_, err := resolveReadFileRequest(raw)
	return err
}

func resolveReadFileRequest(raw json.RawMessage) (readFileRequest, error) {
	var input readFileRequest
	if err := decodeSingleStrictJSON(raw, &input, "read_file request"); err != nil {
		return readFileRequest{}, fmt.Errorf("parse read_file request: %w", err)
	}
	if err := validateFilePath(input.Path); err != nil {
		return readFileRequest{}, err
	}
	charMode := input.OffsetChar != nil || input.LimitChars != nil
	lineMode := input.OffsetLine != nil || input.LimitLines != nil
	if charMode && lineMode {
		return readFileRequest{}, errors.New(
			"character paging cannot be combined with line paging",
		)
	}
	if charMode {
		if input.OffsetChar == nil {
			value := 0
			input.OffsetChar = &value
		}
		if input.LimitChars == nil {
			value := toolcatalog.ReadFileDefaultChars
			input.LimitChars = &value
		}
		if *input.OffsetChar < 0 || *input.LimitChars < 1 ||
			*input.LimitChars > toolcatalog.ReadFileMaxChars {
			return readFileRequest{}, errors.New("invalid file character range")
		}
	} else {
		if input.OffsetLine == nil {
			value := 1
			input.OffsetLine = &value
		}
		if input.LimitLines == nil {
			value := toolcatalog.ReadFileDefaultLines
			input.LimitLines = &value
		}
		if *input.OffsetLine < 1 || *input.LimitLines < 1 ||
			*input.LimitLines > toolcatalog.ReadFileMaxLines {
			return readFileRequest{}, errors.New("invalid file line range")
		}
	}
	return input, nil
}

func runReadFileAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	input, err := resolveReadFileRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	var content []byte
	var digest, contentType string
	if strings.HasPrefix(input.Path, memorystore.Root+"/") {
		digest, content, err = call.Executor.readMemoryFile(ctx, call.Turn, input.Path)
		if err != nil {
			return nil, err
		}
		contentType = http.DetectContentType(content)
	} else {
		artifactID, err := resolveArtifactPath(input.Path)
		if err != nil {
			return nil, err
		}
		var record artifactstore.ArtifactRecord
		content, record, err = loadArtifactContent(ctx, call, artifactID)
		if err != nil {
			return nil, err
		}
		digest, contentType = record.Digest, record.ContentType
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, errors.New("file must contain UTF-8 text without NUL bytes")
	}
	var result map[string]any
	if input.OffsetChar != nil {
		result = readFileChars(content, input.Path, *input.OffsetChar, *input.LimitChars)
	} else {
		result = readFileLines(content, input.Path, *input.OffsetLine, *input.LimitLines)
	}
	result["content_type"] = contentType
	result["size_bytes"] = len(content)
	result["digest"] = digest
	return completeFileTool(result)
}

func (e Executor) readMemoryFile(ctx context.Context, turn Turn, filePath string) (string, []byte, error) {
	name, relativePath, err := memorystore.ParsePath(filePath)
	if err != nil {
		return "", nil, err
	}
	store, err := e.Store.Memories().Resolve(ctx, turn.ProjectID, name)
	if err != nil {
		return "", nil, err
	}
	return e.Store.Memories().Read(ctx, memorystore.Scope{
		OrgID: turn.OrgID, ProjectID: turn.ProjectID, AgentID: turn.AgentID,
	}, store.ID, relativePath)
}

func loadArtifactContent(
	ctx context.Context,
	call asyncToolContext,
	artifactID uuid.UUID,
) ([]byte, artifactstore.ArtifactRecord, error) {
	if call.Executor.Store == nil || call.Executor.Store.Artifacts() == nil {
		return nil, artifactstore.ArtifactRecord{}, errors.New("artifact storage is not configured")
	}
	record, err := call.Executor.Store.Artifacts().GetArtifact(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		artifactID,
	)
	if err != nil {
		return nil, artifactstore.ArtifactRecord{}, fmt.Errorf("load artifact: %w", err)
	}
	if record.SizeBytes != nil && *record.SizeBytes > toolcatalog.MaxReadableArtifactBytes {
		return nil, artifactstore.ArtifactRecord{}, fmt.Errorf(
			"artifact exceeds the %d-byte readable limit",
			toolcatalog.MaxReadableArtifactBytes,
		)
	}
	content, record, err := call.Executor.Store.Artifacts().GetArtifactBlob(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		artifactID,
	)
	if err != nil {
		return nil, artifactstore.ArtifactRecord{}, fmt.Errorf("read artifact: %w", err)
	}
	if len(content) > toolcatalog.MaxReadableArtifactBytes {
		return nil, artifactstore.ArtifactRecord{}, errors.New("artifact exceeds readable limit")
	}
	return content, record, nil
}

func readFileChars(
	content []byte,
	path string,
	offset, limit int,
) map[string]any {
	start, position := 0, 0
	for position < offset && start < len(content) {
		_, size := utf8.DecodeRune(content[start:])
		start += size
		position++
	}
	end, count := start, 0
	for count < limit && end < len(content) {
		_, size := utf8.DecodeRune(content[end:])
		if end-start+size > toolcatalog.FilePageBytes {
			break
		}
		end += size
		count++
	}
	result := map[string]any{
		"path":        path,
		"content":     string(content[start:end]),
		"offset_char": position,
		"bytes_read":  end - start,
		"has_more":    end < len(content),
	}
	if end < len(content) {
		result["next_offset_char"] = position + count
	}
	return result
}

func readFileLines(content []byte, path string, offsetLine, limitLines int) map[string]any {
	start, lineNumber := 0, 1
	for lineNumber < offsetLine && start < len(content) {
		next := bytes.IndexByte(content[start:], '\n')
		if next < 0 {
			start = len(content)
			break
		}
		start += next + 1
		lineNumber++
	}
	end, count := start, 0
	for end < len(content) && count < limitLines {
		length := bytes.IndexByte(content[end:], '\n')
		if length < 0 {
			length = len(content) - end
		} else {
			length++
		}
		if end-start+length > toolcatalog.FilePageBytes {
			break
		}
		end += length
		count++
	}
	result := map[string]any{
		"path":        path,
		"offset_line": offsetLine,
		"lines_read":  count,
	}
	if count == 0 && start < len(content) {
		chunk := content[start:min(start+toolcatalog.FilePageBytes+utf8.UTFMax, len(content))]
		end = start + len(textutil.TruncateBytes(string(chunk), toolcatalog.FilePageBytes))
		result["notice"] = "The requested line exceeds one page; continue in character mode."
	}
	result["content"] = string(content[start:end])
	result["bytes_read"] = end - start
	result["has_more"] = end < len(content)
	if end < len(content) {
		if count == 0 {
			result["next_offset_char"] = utf8.RuneCount(content[:end])
		} else {
			result["next_offset_line"] = offsetLine + count
		}
	}
	return result
}

func completeFileTool(value any) (asyncPhaseResult, error) {
	content, err := structuredToolResultContent(value)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func validateFilePath(path string) error {
	if strings.HasPrefix(path, memorystore.Root+"/") {
		_, _, err := memorystore.ParsePath(path)
		return err
	}
	if _, err := resolveArtifactPath(path); err != nil {
		return errors.New("path must be /artifacts/<artifact_id> or /memory/<store>/<file>")
	}
	return nil
}

func resolveArtifactPath(path string) (uuid.UUID, error) {
	id, ok := strings.CutPrefix(path, toolcatalog.ArtifactVFSRoot+"/")
	if !ok || strings.Contains(id, "/") {
		return uuid.Nil, errors.New("path must be /artifacts/<artifact_id>")
	}
	return publicid.Decode(publicid.KindArtifact, id)
}
