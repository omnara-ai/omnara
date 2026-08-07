package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
	"github.com/omnara-ai/omnara/internal/tooloutput"
)

type artifactRollbackScopeKey struct{}

type artifactRollbackScope struct {
	cleanups  []artifactstore.RollbackCleanup
	committed bool
}

func withArtifactRollbackScope(ctx context.Context) (context.Context, *artifactRollbackScope) {
	scope := &artifactRollbackScope{}
	return context.WithValue(ctx, artifactRollbackScopeKey{}, scope), scope
}

func (scope *artifactRollbackScope) commit() {
	if scope != nil {
		scope.committed = true
		scope.cleanups = nil
	}
}

func (scope *artifactRollbackScope) rollback(ctx context.Context) {
	if scope == nil || scope.committed {
		return
	}
	for index := len(scope.cleanups) - 1; index >= 0; index-- {
		scope.cleanups[index](context.WithoutCancel(ctx))
	}
	scope.cleanups = nil
}

func registerArtifactRollback(
	ctx context.Context,
	cleanup artifactstore.RollbackCleanup,
) error {
	if cleanup == nil {
		return nil
	}
	scope, _ := ctx.Value(artifactRollbackScopeKey{}).(*artifactRollbackScope)
	if scope == nil {
		cleanup(context.WithoutCancel(ctx))
		return errors.New("artifact rollback scope is required")
	}
	scope.cleanups = append(scope.cleanups, cleanup)
	return nil
}

func normalizeToolResultParts(parts json.RawMessage) (json.RawMessage, error) {
	if len(parts) == 0 {
		parts = json.RawMessage(`[]`)
	}
	blocks, err := parseToolResultContentBlocks(parts)
	if err != nil {
		return nil, fmt.Errorf("invalid model-visible content parts: %w", err)
	}
	canonical, err := marshalToolResultContentBlocks(blocks)
	if err != nil {
		return nil, fmt.Errorf("canonicalize model-visible content parts: %w", err)
	}
	return canonical, nil
}

func (s *Store) prepareToolResult(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, toolCallID ID,
	toolName string,
	outcome ToolResultOutcome,
	parts json.RawMessage,
) (json.RawMessage, error) {
	canonical, err := normalizeToolResultParts(parts)
	if err != nil {
		return nil, err
	}
	if len(canonical) <= tooloutput.MaxInlineToolResultBytes ||
		toolcatalog.IsBoundedPullToolName(toolName) {
		return canonical, nil
	}
	if s == nil || s.artifacts == nil {
		return nil, errors.New("artifact storage is required for oversized tool results")
	}
	rewritten, err := tooloutput.RewriteOversized(
		canonical,
		outcome == ToolResultOutcomeSucceeded,
		func(partIndex int, contentType string, content []byte, lineCount int) (tooloutput.Artifact, error) {
			return s.persistToolResultArtifact(
				ctx,
				tx,
				projectID,
				agentID,
				toolCallID,
				partIndex,
				contentType,
				content,
				lineCount,
			)
		},
	)
	if err != nil {
		return nil, fmt.Errorf("offload oversized tool result: %w", err)
	}
	return rewritten, nil
}

func (s *Store) prepareToolResultByID(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, toolCallID ID,
	outcome ToolResultOutcome,
	parts json.RawMessage,
) (json.RawMessage, error) {
	toolCall, err := getToolCallTx(ctx, tx, projectID, agentID, toolCallID)
	if err != nil {
		return nil, err
	}
	return s.prepareToolResult(
		ctx,
		tx,
		projectID,
		agentID,
		toolCallID,
		toolCall.Name,
		outcome,
		parts,
	)
}

func (s *Store) persistToolResultArtifact(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID, toolCallID ID,
	partIndex int,
	contentType string,
	content []byte,
	lineCount int,
) (tooloutput.Artifact, error) {
	digest := blobstore.ContentDigest(content)
	idempotencyKey := fmt.Sprintf(
		"tool-result:%s:part:%d:%s",
		toolCallID,
		partIndex,
		digest,
	)
	filename := "tool-result.txt"
	if strings.EqualFold(contentType, tooloutput.StructuredContentType) {
		filename = "tool-result.json"
	}
	record, cleanup, err := s.artifacts.CreateArtifactInTx(ctx, tx, artifactstore.CreateArtifactInput{
		ProjectID:      projectID,
		AgentID:        agentID,
		ContentType:    contentType,
		Filename:       filename,
		Content:        content,
		MaxBytes:       tooloutput.MaxReadableArtifactBytes,
		IdempotencyKey: idempotencyKey,
	})
	if err != nil {
		return tooloutput.Artifact{}, err
	}
	if err := registerArtifactRollback(ctx, cleanup); err != nil {
		return tooloutput.Artifact{}, err
	}
	if record.Digest != digest || record.ContentType != contentType ||
		record.Filename != filename || record.SizeBytes == nil ||
		*record.SizeBytes != int64(len(content)) {
		return tooloutput.Artifact{}, storeerr.ErrIdempotencyConflict
	}
	artifactID, err := publicid.Encode(publicid.KindArtifact, record.ID)
	if err != nil {
		return tooloutput.Artifact{}, fmt.Errorf("encode artifact id: %w", err)
	}
	return tooloutput.Artifact{
		RawID:       record.ID.String(),
		PublicID:    artifactID,
		ContentType: record.ContentType,
		SizeBytes:   *record.SizeBytes,
		LineCount:   lineCount,
	}, nil
}
