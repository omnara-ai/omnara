package tools

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const searchLineBytes = 256

type readFileRequest struct {
	Path       string `json:"path"`
	OffsetLine *int   `json:"offset_line,omitempty"`
	LimitLines *int   `json:"limit_lines,omitempty"`
	OffsetChar *int   `json:"offset_char,omitempty"`
	LimitChars *int   `json:"limit_chars,omitempty"`
}

type searchFilesRequest struct {
	Path         string `json:"path"`
	Pattern      string `json:"pattern"`
	OffsetLine   *int   `json:"offset_line,omitempty"`
	MaxMatches   *int   `json:"max_matches,omitempty"`
	ContextLines int    `json:"context_lines,omitempty"`
}

func validateReadFileInput(raw json.RawMessage) error {
	_, _, err := resolveReadFileRequest(raw)
	return err
}

func resolveReadFileRequest(raw json.RawMessage) (readFileRequest, uuid.UUID, error) {
	var input readFileRequest
	if err := decodeSingleStrictJSON(raw, &input, "read_file request"); err != nil {
		return readFileRequest{}, uuid.Nil, fmt.Errorf("parse read_file request: %w", err)
	}
	id, err := resolveArtifactPath(input.Path)
	if err != nil {
		return readFileRequest{}, uuid.Nil, errors.New("path must be /artifacts/<artifact_id>")
	}
	charMode := input.OffsetChar != nil || input.LimitChars != nil
	lineMode := input.OffsetLine != nil || input.LimitLines != nil
	if charMode && lineMode {
		return readFileRequest{}, uuid.Nil, errors.New(
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
			return readFileRequest{}, uuid.Nil, errors.New("invalid artifact character range")
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
			return readFileRequest{}, uuid.Nil, errors.New("invalid artifact line range")
		}
	}
	return input, id, nil
}

func validateSearchFilesInput(raw json.RawMessage) error {
	_, _, _, err := resolveSearchFilesRequest(raw)
	return err
}

func resolveSearchFilesRequest(
	raw json.RawMessage,
) (searchFilesRequest, uuid.UUID, *regexp.Regexp, error) {
	var input searchFilesRequest
	if err := decodeSingleStrictJSON(raw, &input, "search_files request"); err != nil {
		return searchFilesRequest{}, uuid.Nil, nil, fmt.Errorf("parse search_files request: %w", err)
	}
	id, err := resolveArtifactPath(input.Path)
	if err != nil {
		return searchFilesRequest{}, uuid.Nil, nil, errors.New(
			"path must be /artifacts/<artifact_id>",
		)
	}
	if input.Pattern == "" || len(input.Pattern) > toolcatalog.SearchMaxPatternBytes {
		return searchFilesRequest{}, uuid.Nil, nil, errors.New("pattern must contain 1–1024 bytes")
	}
	pattern, err := regexp.Compile(input.Pattern)
	if err != nil {
		return searchFilesRequest{}, uuid.Nil, nil, fmt.Errorf("compile pattern: %w", err)
	}
	if input.OffsetLine == nil {
		value := 1
		input.OffsetLine = &value
	}
	if input.MaxMatches == nil {
		value := toolcatalog.SearchDefaultMatches
		input.MaxMatches = &value
	}
	if *input.OffsetLine < 1 || *input.MaxMatches < 1 || *input.MaxMatches > toolcatalog.SearchMaxMatches ||
		input.ContextLines < 0 || input.ContextLines > toolcatalog.SearchMaxContextLines {
		return searchFilesRequest{}, uuid.Nil, nil, errors.New("invalid artifact search limits")
	}
	return input, id, pattern, nil
}

func runReadFileAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	input, artifactID, err := resolveReadFileRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	content, record, err := loadReadableArtifact(ctx, call, artifactID)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if input.OffsetChar != nil {
		result = readFileChars(content, input.Path, *input.OffsetChar, *input.LimitChars)
	} else {
		result = readFileLines(content, input.Path, *input.OffsetLine, *input.LimitLines)
	}
	result["content_type"] = record.ContentType
	result["size_bytes"] = len(content)
	return completeFileTool(result)
}

func runSearchFilesAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	input, artifactID, pattern, err := resolveSearchFilesRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	content, _, err := loadReadableArtifact(ctx, call, artifactID)
	if err != nil {
		return nil, err
	}
	result := searchFilesLines(
		content,
		input.Path,
		pattern,
		*input.OffsetLine,
		*input.MaxMatches,
		input.ContextLines,
	)
	return completeFileTool(result)
}

func loadReadableArtifact(
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
	if !utf8.Valid(content) || bytes.IndexByte(content, 0) >= 0 {
		return nil, artifactstore.ArtifactRecord{}, errors.New("artifact must contain UTF-8 text without NUL bytes")
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
		if end-start+size > toolcatalog.ArtifactPageBytes {
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
		if end-start+length > toolcatalog.ArtifactPageBytes {
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
		chunk := content[start:min(start+toolcatalog.ArtifactPageBytes+utf8.UTFMax, len(content))]
		end = start + len(textutil.TruncateBytes(string(chunk), toolcatalog.ArtifactPageBytes))
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

func searchFilesLines(
	content []byte,
	path string,
	pattern *regexp.Regexp,
	offsetLine, maxMatches, contextLines int,
) map[string]any {
	blocks := make([]map[string]any, 0, maxMatches)
	renderedBytes, lineNumber, byteOffset := 0, 0, 0
	nextOffsetLine := 0
	for line := range bytes.SplitSeq(content, []byte{'\n'}) {
		lineNumber++
		lineStart := byteOffset
		if lineStart >= len(content) {
			break
		}
		byteOffset += len(line) + 1
		if lineNumber < offsetLine {
			continue
		}
		location := pattern.FindIndex(line)
		if location == nil {
			continue
		}
		if len(blocks) >= maxMatches {
			nextOffsetLine = lineNumber
			break
		}
		start, firstLine := lineStart, lineNumber
		for n := 0; n < contextLines && start > 0; n++ {
			start = bytes.LastIndexByte(content[:start-1], '\n') + 1
			firstLine--
		}
		end := min(byteOffset, len(content))
		for n := 0; n < contextLines && end < len(content); n++ {
			next := bytes.IndexByte(content[end:], '\n')
			if next < 0 {
				end = len(content)
				break
			}
			end += next + 1
		}
		visible := make([]map[string]any, 0, 2*contextLines+1)
		blockBytes, currentLine := 0, firstLine
		segment := bytes.TrimSuffix(content[start:end], []byte{'\n'})
		for line := range bytes.SplitSeq(segment, []byte{'\n'}) {
			var text string
			if currentLine == lineNumber {
				text = truncateAroundMatch(line, location[0], location[1], searchLineBytes)
			} else {
				text = textutil.TruncateBytes(string(line[:min(len(line), searchLineBytes+utf8.UTFMax)]), searchLineBytes)
			}
			visible = append(visible, map[string]any{
				"line_number": currentLine,
				"text":        text,
				"is_match":    currentLine == lineNumber,
			})
			blockBytes += len(text) + 32
			currentLine++
		}
		if renderedBytes+blockBytes > toolcatalog.ArtifactPageBytes {
			nextOffsetLine = lineNumber
			break
		}
		blocks = append(blocks, map[string]any{
			"match_line": lineNumber,
			"lines":      visible,
		})
		renderedBytes += blockBytes
	}
	result := map[string]any{
		"path":        path,
		"pattern":     pattern.String(),
		"matches":     blocks,
		"match_count": len(blocks),
		"truncated":   nextOffsetLine > 0,
	}
	if nextOffsetLine > 0 {
		result["next_offset_line"] = nextOffsetLine
	}
	return result
}

func truncateAroundMatch(value []byte, matchStart, matchEnd, limit int) string {
	if len(value) <= limit {
		return string(value)
	}
	matchLength := matchEnd - matchStart
	if matchLength >= limit {
		return textutil.TruncateBytes(string(value[matchStart:min(matchEnd, matchStart+limit+utf8.UTFMax)]), limit)
	}
	remaining := limit - matchLength
	start := max(0, matchStart-remaining/2)
	end := min(len(value), matchEnd+(remaining-(matchStart-start)))
	if end-start < limit {
		start = max(0, end-limit)
	}
	for start < matchStart && !utf8.RuneStart(value[start]) {
		start++
	}
	for end > matchEnd && end < len(value) && !utf8.RuneStart(value[end]) {
		end--
	}
	prefix := ""
	suffix := ""
	if start > 0 {
		prefix = "…"
	}
	if end < len(value) {
		suffix = "…"
	}
	return prefix + string(value[start:end]) + suffix
}

func completeFileTool(value map[string]any) (asyncPhaseResult, error) {
	content, err := structuredToolResultContent(value)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func resolveArtifactPath(path string) (uuid.UUID, error) {
	id, ok := strings.CutPrefix(path, toolcatalog.ArtifactVFSRoot+"/")
	if !ok || strings.Contains(id, "/") {
		return uuid.Nil, errors.New("path must be /artifacts/<artifact_id>")
	}
	return publicid.Decode(publicid.KindArtifact, id)
}
