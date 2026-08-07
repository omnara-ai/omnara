package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/tooloutput"
)

type readArtifactInput struct {
	ArtifactID string `json:"artifact_id"`
	OffsetLine *int   `json:"offset_line,omitempty"`
	LimitLines *int   `json:"limit_lines,omitempty"`
	OffsetByte *int   `json:"offset_byte,omitempty"`
	LimitBytes *int   `json:"limit_bytes,omitempty"`
}

type searchArtifactInput struct {
	ArtifactID   string `json:"artifact_id"`
	Pattern      string `json:"pattern"`
	MaxMatches   *int   `json:"max_matches,omitempty"`
	ContextLines *int   `json:"context_lines,omitempty"`
}

func validateReadArtifactInput(raw json.RawMessage) error {
	_, _, err := resolveReadArtifactInput(raw)
	return err
}

func resolveReadArtifactInput(raw json.RawMessage) (readArtifactInput, storage.ID, error) {
	var input readArtifactInput
	if err := decodeSingleStrictJSON(raw, &input, "read_artifact request"); err != nil {
		return readArtifactInput{}, storage.NilID, err
	}
	id, err := publicid.Decode(publicid.KindArtifact, strings.TrimSpace(input.ArtifactID))
	if err != nil {
		return readArtifactInput{}, storage.NilID, errors.New("artifact_id must be a valid public artifact ID")
	}
	byteMode := input.OffsetByte != nil || input.LimitBytes != nil
	lineMode := input.OffsetLine != nil || input.LimitLines != nil
	if byteMode && lineMode {
		return readArtifactInput{}, storage.NilID, errors.New(
			"byte paging cannot be combined with line paging",
		)
	}
	if byteMode {
		if input.OffsetByte == nil {
			value := 0
			input.OffsetByte = &value
		}
		if input.LimitBytes == nil {
			value := tooloutput.ArtifactPageBytes
			input.LimitBytes = &value
		}
		if *input.OffsetByte < 0 || *input.LimitBytes < 1 ||
			*input.LimitBytes > tooloutput.ArtifactPageBytes {
			return readArtifactInput{}, storage.NilID, errors.New("invalid artifact byte range")
		}
	} else {
		if input.OffsetLine == nil {
			value := 1
			input.OffsetLine = &value
		}
		if input.LimitLines == nil {
			value := tooloutput.ReadArtifactDefaultLines
			input.LimitLines = &value
		}
		if *input.OffsetLine < 1 || *input.LimitLines < 1 ||
			*input.LimitLines > tooloutput.ReadArtifactDefaultLines {
			return readArtifactInput{}, storage.NilID, errors.New("invalid artifact line range")
		}
	}
	return input, id, nil
}

func validateSearchArtifactInput(raw json.RawMessage) error {
	_, _, _, err := resolveSearchArtifactInput(raw)
	return err
}

func resolveSearchArtifactInput(
	raw json.RawMessage,
) (searchArtifactInput, storage.ID, *regexp.Regexp, error) {
	var input searchArtifactInput
	if err := decodeSingleStrictJSON(raw, &input, "search_artifact request"); err != nil {
		return searchArtifactInput{}, storage.NilID, nil, err
	}
	id, err := publicid.Decode(publicid.KindArtifact, strings.TrimSpace(input.ArtifactID))
	if err != nil {
		return searchArtifactInput{}, storage.NilID, nil, errors.New(
			"artifact_id must be a valid public artifact ID",
		)
	}
	if input.Pattern == "" {
		return searchArtifactInput{}, storage.NilID, nil, errors.New("pattern is required")
	}
	pattern, err := regexp.Compile(input.Pattern)
	if err != nil {
		return searchArtifactInput{}, storage.NilID, nil, fmt.Errorf("compile pattern: %w", err)
	}
	if input.MaxMatches == nil {
		value := 20
		input.MaxMatches = &value
	}
	if input.ContextLines == nil {
		value := 0
		input.ContextLines = &value
	}
	if *input.MaxMatches < 1 || *input.MaxMatches > tooloutput.SearchArtifactMaxMatches ||
		*input.ContextLines < 0 || *input.ContextLines > 5 {
		return searchArtifactInput{}, storage.NilID, nil, errors.New("invalid artifact search limits")
	}
	return input, id, pattern, nil
}

func runReadArtifactAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	input, artifactID, err := resolveReadArtifactInput(call.Call.Input)
	if err != nil {
		return failArtifactTool(err)
	}
	content, record, err := loadReadableArtifact(ctx, call, artifactID)
	if err != nil {
		return failArtifactTool(err)
	}
	publicArtifactID, err := publicid.Encode(publicid.KindArtifact, artifactID)
	if err != nil {
		return nil, err
	}
	var result map[string]any
	if input.OffsetByte != nil {
		result, err = readArtifactBytes(content, publicArtifactID, *input.OffsetByte, *input.LimitBytes)
	} else {
		result = readArtifactLines(content, publicArtifactID, *input.OffsetLine, *input.LimitLines)
	}
	if err != nil {
		return failArtifactTool(err)
	}
	result["content_type"] = record.ContentType
	result["size_bytes"] = len(content)
	return completeArtifactTool(result)
}

func runSearchArtifactAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	input, artifactID, pattern, err := resolveSearchArtifactInput(call.Call.Input)
	if err != nil {
		return failArtifactTool(err)
	}
	content, _, err := loadReadableArtifact(ctx, call, artifactID)
	if err != nil {
		return failArtifactTool(err)
	}
	publicArtifactID, err := publicid.Encode(publicid.KindArtifact, artifactID)
	if err != nil {
		return nil, err
	}
	result := searchArtifactLines(
		content,
		publicArtifactID,
		pattern,
		*input.MaxMatches,
		*input.ContextLines,
	)
	return completeArtifactTool(result)
}

func loadReadableArtifact(
	ctx context.Context,
	call asyncToolContext,
	artifactID storage.ID,
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
	if record.SizeBytes == nil || *record.SizeBytes > tooloutput.MaxReadableArtifactBytes {
		return nil, artifactstore.ArtifactRecord{}, fmt.Errorf(
			"artifact exceeds the %d-byte readable limit",
			tooloutput.MaxReadableArtifactBytes,
		)
	}
	if !tooloutput.IsTextReadableContentType(record.ContentType) {
		return nil, artifactstore.ArtifactRecord{}, fmt.Errorf(
			"artifact content type %q is not text-readable",
			record.ContentType,
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
	if !utf8.Valid(content) {
		return nil, artifactstore.ArtifactRecord{}, errors.New("artifact is not valid UTF-8 text")
	}
	return content, record, nil
}

func readArtifactBytes(
	content []byte,
	artifactID string,
	offset, limit int,
) (map[string]any, error) {
	if offset > len(content) {
		offset = len(content)
	}
	if offset < len(content) && !utf8.RuneStart(content[offset]) {
		return nil, errors.New("offset_byte must point to a UTF-8 character boundary")
	}
	end := min(offset+limit, len(content))
	for end > offset && end < len(content) && !utf8.RuneStart(content[end]) {
		end--
	}
	result := map[string]any{
		"artifact_id": artifactID,
		"content":     string(content[offset:end]),
		"offset_byte": offset,
		"bytes_read":  end - offset,
		"has_more":    end < len(content),
	}
	if end < len(content) {
		result["next_offset_byte"] = end
	}
	return result, nil
}

func readArtifactLines(
	content []byte,
	artifactID string,
	offsetLine, limitLines int,
) map[string]any {
	lines := tooloutput.Lines(content)
	start := min(offsetLine-1, len(lines))
	end := min(start+limitLines, len(lines))
	selected := lines[start:end]
	var rendered strings.Builder
	consumed := 0
	for index, line := range selected {
		extra := len(line)
		if index > 0 {
			extra++
		}
		if consumed+extra > tooloutput.ArtifactPageBytes {
			if index == 0 {
				lineStart := byteOffsetForLine(lines, start)
				chunk := tooloutput.TruncateBytes(line, tooloutput.ArtifactPageBytes)
				return map[string]any{
					"artifact_id":      artifactID,
					"content":          chunk,
					"offset_line":      offsetLine,
					"lines_read":       0,
					"bytes_read":       len(chunk),
					"has_more":         true,
					"next_offset_byte": lineStart + len(chunk),
					"notice":           "The requested line is larger than one page; continue in byte mode.",
				}
			}
			end = start + index
			break
		}
		if index > 0 {
			rendered.WriteByte('\n')
		}
		rendered.WriteString(line)
		consumed += extra
	}
	result := map[string]any{
		"artifact_id": artifactID,
		"content":     rendered.String(),
		"offset_line": offsetLine,
		"lines_read":  end - start,
		"bytes_read":  consumed,
		"has_more":    end < len(lines),
	}
	if end < len(lines) {
		result["next_offset_line"] = end + 1
	}
	return result
}

func byteOffsetForLine(lines []string, lineIndex int) int {
	offset := 0
	for _, line := range lines[:lineIndex] {
		offset += len(line) + 1
	}
	return offset
}

func searchArtifactLines(
	content []byte,
	artifactID string,
	pattern *regexp.Regexp,
	maxMatches, contextLines int,
) map[string]any {
	lines := tooloutput.Lines(content)
	blocks := make([]map[string]any, 0, maxMatches)
	renderedBytes := 0
	hasMore := false
	for index, line := range lines {
		location := pattern.FindStringIndex(line)
		if location == nil {
			continue
		}
		if len(blocks) >= maxMatches {
			hasMore = true
			break
		}
		start := max(0, index-contextLines)
		end := min(len(lines), index+contextLines+1)
		visible := make([]map[string]any, 0, end-start)
		blockBytes := 0
		for lineIndex := start; lineIndex < end; lineIndex++ {
			text := lines[lineIndex]
			if lineIndex == index {
				text = truncateAroundMatch(text, location[0], location[1], tooloutput.SearchArtifactLineBytes)
			} else {
				text = tooloutput.TruncateBytes(text, tooloutput.SearchArtifactLineBytes)
			}
			visible = append(visible, map[string]any{
				"line_number": lineIndex + 1,
				"text":        text,
				"is_match":    lineIndex == index,
			})
			blockBytes += len(text) + 32
		}
		if renderedBytes+blockBytes > tooloutput.ArtifactPageBytes {
			hasMore = true
			break
		}
		blocks = append(blocks, map[string]any{
			"match_line": index + 1,
			"lines":      visible,
		})
		renderedBytes += blockBytes
	}
	summary := fmt.Sprintf("Found %d matching lines.", len(blocks))
	if hasMore {
		summary = fmt.Sprintf("Found at least %d matching lines; results were truncated.", len(blocks))
	}
	return map[string]any{
		"artifact_id": artifactID,
		"pattern":     pattern.String(),
		"matches":     blocks,
		"match_count": len(blocks),
		"truncated":   hasMore,
		"summary":     summary,
	}
}

func truncateAroundMatch(value string, matchStart, matchEnd, limit int) string {
	if len(value) <= limit {
		return value
	}
	matchLength := matchEnd - matchStart
	if matchLength >= limit {
		return tooloutput.TruncateBytes(value[matchStart:matchEnd], limit)
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
	return prefix + value[start:end] + suffix
}

func completeArtifactTool(value map[string]any) (asyncPhaseResult, error) {
	content, err := structuredToolResultContent(value)
	if err != nil {
		return nil, err
	}
	return completeAsynchronously(content), nil
}

func failArtifactTool(cause error) (asyncPhaseResult, error) {
	content, err := structuredToolResultContent(map[string]any{
		"error": cause.Error(),
	})
	if err != nil {
		return nil, err
	}
	return failAsynchronously(content, cause), nil
}
