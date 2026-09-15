package executionstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/jsoncanonical"
	"github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

// PreparedInputContent keeps network/blob work outside the owning agent-input
// transaction. Attachment ordinals replace their original inline media blocks
// with canonical artifact references after persistence, including on replay.
type PreparedInputContent struct {
	Blocks      json.RawMessage
	Attachments []PreparedInputAttachment
}

type PreparedInputAttachment struct {
	Ordinal  int
	Artifact *artifactstore.PreparedArtifact
	Metadata json.RawMessage
}

func (s *Store) persistInputContentTx(
	ctx context.Context,
	tx pgx.Tx,
	projectID, agentID ID,
	content PreparedInputContent,
) ([]CreateContentBlockInput, json.RawMessage, error) {
	if len(content.Attachments) > 20 {
		return nil, nil, storeerr.InvalidRequest(errors.New("an input cannot contain more than 20 attachments"))
	}
	if _, err := jsoncanonical.Decode(content.Blocks); err != nil {
		return nil, nil, storeerr.InvalidRequest(err)
	}
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(content.Blocks, &rawBlocks); err != nil || len(rawBlocks) == 0 || len(rawBlocks) > 100 {
		return nil, nil, storeerr.InvalidRequest(errors.New("input must contain between 1 and 100 content blocks"))
	}
	seen := make(map[int]bool, len(content.Attachments))
	for _, attachment := range content.Attachments {
		if attachment.Ordinal < 0 || attachment.Ordinal >= len(rawBlocks) ||
			seen[attachment.Ordinal] || attachment.Artifact == nil {
			return nil, nil, storeerr.InvalidRequest(errors.New("attachment ordinal must identify one unique content block"))
		}
		seen[attachment.Ordinal] = true
	}
	if len(content.Attachments) > 0 && s.artifacts == nil {
		return nil, nil, errors.New("artifact store is required for prepared input attachments")
	}
	for _, attachment := range content.Attachments {
		artifact, err := s.artifacts.PersistPreparedArtifact(ctx, tx, attachment.Artifact)
		if err != nil {
			return nil, nil, fmt.Errorf("persist input attachment: %w", err)
		}
		if artifact.ProjectID != projectID || artifact.AgentID != agentID {
			return nil, nil, storeerr.ErrUnauthorized
		}
		block, err := json.Marshal(struct {
			Type       string          `json:"type"`
			ArtifactID string          `json:"artifact_id"`
			Metadata   json.RawMessage `json:"metadata,omitempty"`
		}{"media_ref", artifact.ID.String(), attachment.Metadata})
		if err != nil {
			return nil, nil, fmt.Errorf("encode input attachment: %w", err)
		}
		rawBlocks[attachment.Ordinal] = block
	}
	raw, err := json.Marshal(rawBlocks)
	if err != nil {
		return nil, nil, err
	}
	blocks, err := parseAgentInputContentBlocks(raw)
	if err != nil {
		return nil, nil, err
	}
	canonical, err := marshalAgentInputContentBlocks(blocks)
	return blocks, canonical, err
}

// Cleanup cannot change a committed input into a failed/retryable operation.
// The outer transaction chooses one outcome for all its prepared attachments.
func (s *Store) finishInputContent(
	ctx context.Context,
	content PreparedInputContent,
	outcome artifactstore.ArtifactTransactionOutcome,
) {
	if len(content.Attachments) == 0 || s.artifacts == nil {
		return
	}
	prepared := make([]*artifactstore.PreparedArtifact, 0, len(content.Attachments))
	for _, attachment := range content.Attachments {
		prepared = append(prepared, attachment.Artifact)
	}
	if err := s.artifacts.FinishPreparedArtifacts(ctx, outcome, prepared...); err != nil {
		event := log.NewEvent(ctx, "channel.input.artifact_cleanup", log.Fields{})
		event.Level(log.WarnLevel)
		event.Error(err)
		event.Done(ctx)
	}
}
