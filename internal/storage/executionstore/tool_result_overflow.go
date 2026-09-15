package executionstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/textutil"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

const (
	ToolResultInlineBudgetBytes = 50 * 1024
	toolResultPreviewBytes      = 8 * 1024
)

func overflowArtifactKey(callID uuid.UUID, canonical json.RawMessage) string {
	return "tool-overflow:" + callID.String() + ":" + blobstore.ContentDigest(canonical)
}

type toolResultOverflow struct {
	content        []byte
	contentType    string
	hidden         bool
	retainedBlocks []CreateContentBlockInput
}

func prepareToolResultOverflow(
	blocks []CreateContentBlockInput,
	canonical json.RawMessage,
) (toolResultOverflow, error) {
	if len(canonical) <= ToolResultInlineBudgetBytes {
		return toolResultOverflow{}, nil
	}
	hasText := false
	for _, block := range blocks {
		if block.BlockKind == ContentBlockKindText || block.BlockKind == ContentBlockKindStructuredData {
			hasText = true
		}
	}
	if !hasText {
		return toolResultOverflow{}, nil
	}
	remaining, err := marshalToolResultContentBlocks(blocks[1:])
	if err != nil {
		return toolResultOverflow{}, err
	}
	overflow := toolResultOverflow{}
	if blocks[0].BlockKind == ContentBlockKindText && len(blocks[0].Metadata) == 0 &&
		len(remaining)+toolResultPreviewBytes+1024 <= ToolResultInlineBudgetBytes {
		overflow.content = []byte(blocks[0].TextContent)
		overflow.contentType = "text/plain"
		overflow.retainedBlocks = blocks[1:]
	} else {
		var formatted bytes.Buffer
		if err := json.Indent(&formatted, canonical, "", "  "); err != nil {
			return toolResultOverflow{}, err
		}
		overflow.content = formatted.Bytes()
		overflow.contentType = "application/json"
		for _, block := range blocks {
			if block.Metadata["omnara_hidden"] == "true" {
				overflow.hidden = true
			}
			if block.BlockKind == ContentBlockKindArtifact {
				overflow.retainedBlocks = append(overflow.retainedBlocks, block)
			}
		}
	}
	if len(overflow.content) > toolcatalog.MaxReadableArtifactBytes {
		return toolResultOverflow{}, storeerr.InvalidRequest(
			errors.New("tool output exceeds the readable artifact size limit"),
		)
	}
	return overflow, nil
}

func (overflow *toolResultOverflow) contentParts(id uuid.UUID) (json.RawMessage, error) {
	publicID, err := publicid.Encode(publicid.KindArtifact, id)
	if err != nil {
		return nil, err
	}
	chunk := overflow.content[:min(len(overflow.content), toolResultPreviewBytes+utf8.UTFMax)]
	preview := textutil.TruncateBytes(string(chunk), toolResultPreviewBytes)
	for {
		encoded, err := json.Marshal(preview)
		if err != nil {
			return nil, err
		}
		if len(encoded) <= toolResultPreviewBytes {
			break
		}
		preview = textutil.TruncateBytes(preview, len(preview)*3/4)
	}
	value, err := json.Marshal(map[string]any{
		"path":         toolcatalog.ArtifactVFSRoot + "/" + publicID,
		"preview":      preview,
		"size_bytes":   len(overflow.content),
		"content_type": overflow.contentType,
		"truncated":    true,
	})
	if err != nil {
		return nil, err
	}
	result := []CreateContentBlockInput{
		{BlockKind: ContentBlockKindStructuredData, StructuredData: value},
		{BlockKind: ContentBlockKindArtifact, ArtifactID: id, ExcludeFromModelContext: true},
	}
	if overflow.hidden {
		for index := range result {
			result[index].Metadata = map[string]string{"omnara_hidden": "true"}
		}
	}
	result = append(result, overflow.retainedBlocks...)
	return marshalToolResultContentBlocks(result)
}

func shouldOffloadToolResult(toolType, name string) bool {
	switch toolType {
	case toolcatalog.ToolTypeMCP, toolcatalog.ToolTypeCustom:
		return true
	case toolcatalog.ToolTypeBuiltIn:
		switch name {
		case toolcatalog.ToolNameWebFetch, toolcatalog.ToolNameWebSearch, toolcatalog.ToolNameSkill:
			return true
		}
	}
	return false
}

func (s *Store) loadToolResultForReplay(
	ctx context.Context,
	read *toolResultArtifactReadRequiredError,
) (context.Context, error) {
	if s.artifacts == nil {
		return ctx, errors.New("artifact storage is required for tool result replay")
	}
	content, _, err := s.artifacts.GetArtifactBlob(ctx, read.projectID, read.agentID, read.artifactID)
	if err != nil {
		return ctx, err
	}
	return context.WithValue(ctx, toolResultArtifactContentsKey{}, map[uuid.UUID][]byte{read.artifactID: content}), nil
}

func (s *Store) prepareToolResult(
	ctx context.Context,
	projectID, agentID, callID uuid.UUID,
	parts json.RawMessage,
) (json.RawMessage, error) {
	blocks, err := parseToolResultContentBlocks(parts)
	if err != nil {
		return nil, err
	}
	canonical, err := marshalToolResultContentBlocks(blocks)
	if err != nil {
		return nil, err
	}
	if len(canonical) <= ToolResultInlineBudgetBytes {
		return canonical, nil
	}
	call, err := s.GetToolCall(ctx, projectID, agentID, callID)
	if err != nil {
		return nil, err
	}
	if call.State == ToolCallStateCompleted || !shouldOffloadToolResult(call.Type, call.Name) {
		return canonical, nil
	}
	overflow, err := prepareToolResultOverflow(blocks, canonical)
	if err != nil {
		return nil, err
	}
	if overflow.content == nil {
		return canonical, nil
	}
	key := overflowArtifactKey(callID, canonical)
	artifact, err := s.q.GetArtifactByIdempotencyKey(ctx, dbsqlc.GetArtifactByIdempotencyKeyParams{
		ProjectID:      projectID,
		AgentID:        agentID,
		IdempotencyKey: key,
	})
	if errors.Is(err, pgx.ErrNoRows) {
		if s.artifacts == nil {
			return nil, errors.New("artifact storage is required for oversized tool results")
		}
		created, err := s.artifacts.CreateArtifact(ctx, artifactstore.CreateArtifactInput{
			ProjectID: projectID, AgentID: agentID, ContentType: overflow.contentType,
			Filename: "tool-result", Content: overflow.content, MaxBytes: toolcatalog.MaxReadableArtifactBytes,
			IdempotencyKey: key,
		})
		if err != nil {
			return nil, fmt.Errorf("persist tool overflow: %w", err)
		}
		return overflow.contentParts(created.ID)
	}
	if err != nil {
		return nil, err
	}
	if artifact.Digest != blobstore.ContentDigest(overflow.content) || artifact.ContentType != overflow.contentType ||
		artifact.Filename != "tool-result" || artifact.SizeBytes == nil ||
		*artifact.SizeBytes != int64(len(overflow.content)) {
		return nil, storeerr.ErrIdempotencyConflict
	}
	return overflow.contentParts(artifact.ID)
}

func replayToolResultOverflow(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, callID uuid.UUID,
	blocks []CreateContentBlockInput,
	canonical json.RawMessage,
) (json.RawMessage, error) {
	overflow, err := prepareToolResultOverflow(blocks, canonical)
	if err != nil {
		return nil, err
	}
	if overflow.content == nil {
		return canonical, nil
	}
	record, err := qtx.GetArtifactByIdempotencyKey(ctx, dbsqlc.GetArtifactByIdempotencyKeyParams{
		ProjectID:      projectID,
		AgentID:        agentID,
		IdempotencyKey: overflowArtifactKey(callID, canonical),
	})
	if err != nil {
		return nil, err
	}
	return overflow.contentParts(record.ID)
}

type toolResultArtifactContentsKey struct{}

type toolResultArtifactReadRequiredError struct {
	projectID  uuid.UUID
	agentID    uuid.UUID
	artifactID uuid.UUID
}

func (e *toolResultArtifactReadRequiredError) Error() string {
	return "tool result artifact must be read outside the transaction"
}

func expandToolResultOverflow(
	ctx context.Context,
	projectID, agentID uuid.UUID,
	parts json.RawMessage,
) (json.RawMessage, error) {
	blocks, err := parseToolResultContentBlocks(parts)
	if err != nil {
		return nil, err
	}
	if len(blocks) < 2 || blocks[0].BlockKind != ContentBlockKindStructuredData ||
		blocks[1].BlockKind != ContentBlockKindArtifact || !blocks[1].ExcludeFromModelContext {
		return parts, nil
	}
	var marker struct {
		Path        string `json:"path"`
		Truncated   bool   `json:"truncated"`
		ContentType string `json:"content_type"`
	}
	if err := json.Unmarshal(blocks[0].StructuredData, &marker); err != nil {
		return nil, err
	}
	id, err := publicid.Encode(publicid.KindArtifact, blocks[1].ArtifactID)
	if err != nil {
		return nil, err
	}
	if !marker.Truncated || marker.Path != toolcatalog.ArtifactVFSRoot+"/"+id {
		return parts, nil
	}
	contents, _ := ctx.Value(toolResultArtifactContentsKey{}).(map[uuid.UUID][]byte)
	if content, ok := contents[blocks[1].ArtifactID]; ok {
		if marker.ContentType == "text/plain" {
			return marshalToolResultContentBlocks(append([]CreateContentBlockInput{{
				BlockKind: ContentBlockKindText, TextContent: string(content),
			}}, blocks[2:]...))
		}
		return content, nil
	}
	return nil, &toolResultArtifactReadRequiredError{
		projectID: projectID, agentID: agentID, artifactID: blocks[1].ArtifactID,
	}
}

func toolResultContentMatchesTx(
	ctx context.Context,
	qtx *dbsqlc.Queries,
	projectID, agentID, callID uuid.UUID,
	stored json.RawMessage,
	blocks []CreateContentBlockInput,
	canonical json.RawMessage,
) (bool, error) {
	if sameJSON(stored, canonical) {
		return true, nil
	}
	rewritten, err := replayToolResultOverflow(ctx, qtx, projectID, agentID, callID, blocks, canonical)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return false, err
	}
	if err == nil && sameJSON(stored, rewritten) {
		return true, nil
	}
	expanded, err := expandToolResultOverflow(ctx, projectID, agentID, stored)
	if err != nil {
		return false, err
	}
	return sameJSON(expanded, canonical), nil
}
