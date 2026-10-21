package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"path"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/memorystore"
	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type storageReadRequest struct {
	Path       string `json:"path"`
	OffsetLine *int   `json:"offset_line,omitempty"`
	LimitLines *int   `json:"limit_lines,omitempty"`
	OffsetChar *int   `json:"offset_char,omitempty"`
	LimitChars *int   `json:"limit_chars,omitempty"`
}

func validateStorageReadInput(raw json.RawMessage) error {
	_, err := resolveStorageReadRequest(raw)
	return err
}

func resolveStorageReadRequest(raw json.RawMessage) (storageReadRequest, error) {
	var input storageReadRequest
	if err := decodeSingleStrictJSON(raw, &input, "storage_read request"); err != nil {
		return storageReadRequest{}, fmt.Errorf("parse storage_read request: %w", err)
	}
	if err := validateFilePath(input.Path); err != nil {
		return storageReadRequest{}, err
	}
	charMode := input.OffsetChar != nil || input.LimitChars != nil
	lineMode := input.OffsetLine != nil || input.LimitLines != nil
	if charMode && lineMode {
		return storageReadRequest{}, errors.New(
			"character paging cannot be combined with line paging",
		)
	}
	if charMode {
		if input.OffsetChar == nil {
			value := 0
			input.OffsetChar = &value
		}
		if input.LimitChars == nil {
			value := toolcatalog.StorageReadDefaultChars
			input.LimitChars = &value
		}
		if *input.OffsetChar < 0 || *input.LimitChars < 1 ||
			*input.LimitChars > toolcatalog.StorageReadMaxChars {
			return storageReadRequest{}, errors.New("invalid file character range")
		}
	} else {
		if input.OffsetLine == nil {
			value := 1
			input.OffsetLine = &value
		}
		if input.LimitLines == nil {
			value := toolcatalog.StorageReadDefaultLines
			input.LimitLines = &value
		}
		if *input.OffsetLine < 1 || *input.LimitLines < 1 ||
			*input.LimitLines > toolcatalog.StorageReadMaxLines {
			return storageReadRequest{}, errors.New("invalid file line range")
		}
	}
	return input, nil
}

func runStorageReadAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	input, err := resolveStorageReadRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	var content []byte
	var digest, contentType, note string
	var artifactID uuid.UUID
	if strings.HasPrefix(input.Path, memorystore.Root+"/") {
		file, err := call.Executor.readMemoryFile(ctx, call.Turn, input.Path)
		if err != nil {
			return nil, err
		}
		digest, content = file.Digest, file.Content
		if note, err = call.Executor.memoryReadNote(ctx, call.Turn, input.Path, file); err != nil {
			return nil, err
		}
		contentType = http.DetectContentType(content)
	} else {
		artifactID, err = resolveArtifactPath(input.Path)
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
	if modelcontext.IsViewableImage(contentType, content) {
		if artifactID == uuid.Nil {
			artifact, err := call.Executor.Store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
				ProjectID:      call.Turn.ProjectID,
				AgentID:        call.Turn.AgentID,
				ContentType:    contentType,
				Filename:       path.Base(input.Path),
				Content:        content,
				MaxBytes:       daemonprotocol.MaxFileTransferBytes,
				IdempotencyKey: "memory-image:" + blobstore.ContentDigest([]byte(input.Path+"\x00"+digest)),
			})
			if err != nil {
				return nil, fmt.Errorf("save memory image: %w", err)
			}
			artifactID = artifact.ID
		}
		return completeImageRead(input.Path, contentType, digest, len(content), artifactID, note)
	}
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, errors.New(
			"file must contain UTF-8 text without NUL bytes, or be a PNG, JPEG, GIF, or WebP image",
		)
	}
	var result map[string]any
	if input.OffsetChar != nil {
		result = readFileChars(content, input.Path, *input.OffsetChar, *input.LimitChars)
	} else {
		result = readFileLines(content, input.Path, *input.OffsetLine, *input.LimitLines)
	}
	result["content_type"] = contentType
	result["size_bytes"] = len(content)
	part, err := structuredToolResultPart(result)
	if err != nil {
		return nil, err
	}
	digestPart, err := fileDigestToolResultPart(digest)
	if err != nil {
		return nil, err
	}
	return completeFileRead(note, part, digestPart), nil
}

func (e Executor) readMemoryFile(ctx context.Context, turn Turn, filePath string) (memorystore.File, error) {
	name, relativePath, err := memorystore.ParsePath(filePath)
	if err != nil {
		return memorystore.File{}, err
	}
	store, err := e.Store.Memories().Resolve(ctx, turn.ProjectID, name)
	if err != nil {
		return memorystore.File{}, err
	}
	return e.Store.Memories().ReadFile(ctx, memorystore.Scope{
		OrgID: turn.OrgID, ProjectID: turn.ProjectID, AgentID: turn.AgentID,
	}, store.ID, relativePath)
}

func (e Executor) memoryReadNote(
	ctx context.Context, turn Turn, filePath string, file memorystore.File,
) (string, error) {
	seen, found, err := e.Store.Memories().LastSeen(ctx, turn.AgentID, filePath)
	if err != nil || !found || seen.Digest == file.Digest {
		return "", err
	}
	updated, last := memoryChangeTimes(file.ModifiedAt, seen.At)
	return fmt.Sprintf("note: %s was updated at %s since your last read at %s", filePath, updated, last), nil
}

func memoryChangeTimes(updated, seen time.Time) (string, string) {
	const layout = "2006-01-02 15:04:05 UTC"
	updated, seen = updated.UTC(), seen.UTC()
	if updated.Format(time.DateOnly) == seen.Format(time.DateOnly) {
		return updated.Format(layout), seen.Format(time.TimeOnly + " UTC")
	}
	return updated.Format(layout), seen.Format(layout)
}

func completeFileRead(note string, parts ...toolResultPart) asyncPhaseResult {
	if note != "" {
		parts = append([]toolResultPart{textToolResultPart(note)}, parts...)
	}
	return completeAsynchronously(newToolResultContent(parts...))
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

func completeImageRead(
	path string,
	contentType string,
	digest string,
	sizeBytes int,
	artifactID uuid.UUID,
	note string,
) (asyncPhaseResult, error) {
	metadata, err := structuredToolResultPart(map[string]any{
		"path":         path,
		"content_type": contentType,
		"size_bytes":   sizeBytes,
	})
	if err != nil {
		return nil, err
	}
	media, err := mediaToolResultPart(artifactID)
	if err != nil {
		return nil, err
	}
	digestPart, err := fileDigestToolResultPart(digest)
	if err != nil {
		return nil, err
	}
	return completeFileRead(note, metadata, media, digestPart), nil
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
	if strings.HasPrefix(path, toolcatalog.ArtifactVFSRoot+"/") {
		_, err := resolveArtifactPath(path)
		return err
	}
	return errors.New("path must be /artifacts/<artifact_id> or /memory/<store>/<file>")
}

func resolveArtifactPath(path string) (uuid.UUID, error) {
	id, ok := strings.CutPrefix(path, toolcatalog.ArtifactVFSRoot+"/")
	if ok && !strings.Contains(id, "/") {
		if artifactID, err := publicid.Decode(publicid.KindArtifact, id); err == nil {
			return artifactID, nil
		}
	}
	return uuid.Nil, errors.New(
		"artifacts are read by ID; use storage_list with /artifacts/<filename or glob>, then use a returned /artifacts/<artifact_id> path",
	)
}
