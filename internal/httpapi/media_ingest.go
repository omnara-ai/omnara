package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/blobstore"
	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	maxAttachmentBytes         = 10 * 1024 * 1024
	maxAttachmentsPerInput     = 20
	maxAttachmentFilenameBytes = 255
	maxAttachmentFilenameRunes = 255
	maxTotalAttachmentBytes    = int(modelcontext.MaxResolvedMediaBytes)
	maxContentBlockBytes       = 1024 * 1024
)

type mediaIngestError struct {
	message string
}

func (e mediaIngestError) Error() string { return e.message }

type mediaIngestContext struct {
	ProjectID      uuid.UUID
	AgentID        uuid.UUID
	IdempotencyKey string
}

type pendingAttachment struct {
	ordinal   int
	mediaType string
	filename  string
	content   []byte
	metadata  json.RawMessage
}

type inboundBlock struct {
	Type      string          `json:"type"`
	Data      string          `json:"data,omitempty"`
	MediaType string          `json:"media_type,omitempty"`
	Filename  string          `json:"filename,omitempty"`
	Metadata  json.RawMessage `json:"metadata,omitempty"`
}

func (s *Server) extractInlineMedia(
	ctx context.Context, ingest mediaIngestContext, contentBlocks json.RawMessage,
) (json.RawMessage, error) {
	return ingestInlineMedia(contentBlocks, func(attachment pendingAttachment) (uuid.UUID, error) {
		key := ""
		if ingest.IdempotencyKey != "" {
			key = fmt.Sprintf("upload:%s:%d", ingest.IdempotencyKey, attachment.ordinal)
		}
		artifact, err := s.store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
			ProjectID: ingest.ProjectID, AgentID: ingest.AgentID, ContentType: attachment.mediaType,
			Filename: attachment.filename, Content: attachment.content, MaxBytes: maxAttachmentBytes, IdempotencyKey: key,
		})
		return artifact.ID, err
	})
}

func (s *Server) prepareInputMedia(
	ctx context.Context, agentID uuid.UUID, contentBlocks json.RawMessage,
) (json.RawMessage, []artifactstore.PreparedArtifact, error) {
	var artifacts []artifactstore.PreparedArtifact
	blocks, err := ingestInlineMedia(contentBlocks, func(attachment pendingAttachment) (uuid.UUID, error) {
		artifact := artifactstore.PreparedArtifact{ID: uuid.New(), ContentType: attachment.mediaType,
			Filename: attachment.filename, Digest: blobstore.ContentDigest(attachment.content),
			SizeBytes: int64(len(attachment.content))}
		artifacts = append(artifacts, artifact)
		return artifact.ID, s.store.Artifacts().UploadPreparedArtifact(ctx, agentID, artifact, attachment.content)
	})
	return blocks, artifacts, err
}

func (s *Server) cleanupInputMedia(
	ctx context.Context, projectID, agentID uuid.UUID, artifacts []artifactstore.PreparedArtifact, cause error,
) {
	if len(artifacts) == 0 || errors.Is(cause, storeerr.ErrCommitOutcomeUnknown) {
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 10*time.Second)
	defer cancel()
	for _, artifact := range artifacts {
		if err := s.store.Artifacts().DeleteUnreferencedPreparedArtifact(ctx, projectID, agentID, artifact.ID); err != nil {
			s.log.WarnContext(ctx, "cleanup input attachment", "agent_id", agentID, "artifact_id", artifact.ID, "error", err)
		}
	}
}

func ingestInlineMedia(
	contentBlocks json.RawMessage, store func(pendingAttachment) (uuid.UUID, error),
) (json.RawMessage, error) {
	if len(contentBlocks) == 0 {
		return contentBlocks, nil
	}
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(contentBlocks, &rawBlocks); err != nil {
		return contentBlocks, nil //nolint:nilerr // Canonical block validation owns malformed input errors.
	}
	nonAttachmentBytes := 0
	pending := make(map[int]pendingAttachment)
	totalBytes := 0
	for ordinal, raw := range rawBlocks {
		var block inboundBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			nonAttachmentBytes += len(raw)
			continue
		}
		if block.Type == "media_ref" {
			return nil, mediaIngestError{
				fmt.Sprintf(
					"content block %d: media_ref blocks are output-only; attach media as a base64 `media` block",
					ordinal,
				),
			}
		}
		if block.Type != "media" {
			nonAttachmentBytes += len(raw)
			continue
		}
		if len(block.Metadata) > 0 {
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal(block.Metadata, &metadata); err != nil || metadata == nil {
				return nil, mediaIngestError{
					fmt.Sprintf("content block %d: metadata must be a JSON object", ordinal),
				}
			}
		}
		nonAttachmentBytes += len(block.Metadata)
		if block.Data == "" {
			return nil, mediaIngestError{
				fmt.Sprintf(
					"content block %d: media block requires non-empty base64 data",
					ordinal,
				),
			}
		}
		if !modelcontext.IsAttachmentMedia(block.MediaType) {
			return nil, mediaIngestError{
				fmt.Sprintf(
					"content block %d: unsupported media_type %q",
					ordinal,
					block.MediaType,
				),
			}
		}
		content, err := base64.StdEncoding.DecodeString(block.Data)
		if err != nil {
			return nil, mediaIngestError{
				fmt.Sprintf("content block %d: data must be standard base64", ordinal),
			}
		}
		if len(content) == 0 {
			return nil, mediaIngestError{
				fmt.Sprintf("content block %d: data must not be empty", ordinal),
			}
		}
		if len(content) > maxAttachmentBytes {
			return nil, mediaIngestError{
				fmt.Sprintf(
					"content block %d: attachment exceeds %d bytes",
					ordinal,
					maxAttachmentBytes,
				),
			}
		}
		if modelcontext.IsTextDocumentMediaType(block.MediaType) && !utf8.Valid(content) {
			return nil, mediaIngestError{
				fmt.Sprintf("content block %d: text attachment must be valid UTF-8", ordinal),
			}
		}
		if len(pending) == maxAttachmentsPerInput {
			return nil, mediaIngestError{
				fmt.Sprintf("too many attachments: limit is %d per input", maxAttachmentsPerInput),
			}
		}
		if utf8.RuneCountInString(block.Filename) > maxAttachmentFilenameRunes {
			return nil, mediaIngestError{
				fmt.Sprintf(
					"content block %d: filename exceeds %d characters",
					ordinal,
					maxAttachmentFilenameRunes,
				),
			}
		}
		if err := dbsafe.Text(block.Filename); err != nil {
			return nil, mediaIngestError{
				fmt.Sprintf("content block %d: filename %s", ordinal, err),
			}
		}
		totalBytes += len(content)
		if totalBytes > maxTotalAttachmentBytes {
			return nil, mediaIngestError{
				fmt.Sprintf("attachments exceed %d bytes combined", maxTotalAttachmentBytes),
			}
		}
		pending[ordinal] = pendingAttachment{
			ordinal:   ordinal,
			mediaType: block.MediaType,
			filename:  block.Filename,
			content:   content,
			metadata:  block.Metadata,
		}
	}
	if nonAttachmentBytes > maxContentBlockBytes {
		return nil, mediaIngestError{
			fmt.Sprintf(
				"content blocks exceed %d bytes excluding attachments",
				maxContentBlockBytes,
			),
		}
	}
	if len(pending) == 0 {
		return contentBlocks, nil
	}
	for ordinal, raw := range rawBlocks {
		if attachment, ok := pending[ordinal]; ok {
			raw = attachment.metadata
		}
		if len(raw) == 0 {
			continue
		}
		if err := dbsafe.JSONStrings(raw); err != nil {
			return nil, mediaIngestError{
				fmt.Sprintf("content block %d: %s", ordinal, err),
			}
		}
	}
	out := make([]json.RawMessage, 0, len(rawBlocks))
	for ordinal, raw := range rawBlocks {
		attachment, ok := pending[ordinal]
		if !ok {
			out = append(out, raw)
			continue
		}
		artifactID, err := store(attachment)
		if err != nil {
			return nil, fmt.Errorf("store attachment %d: %w", ordinal, err)
		}
		rewrittenBlock := map[string]any{
			"type":        "media_ref",
			"artifact_id": artifactID.String(),
		}
		if len(attachment.metadata) > 0 {
			rewrittenBlock["metadata"] = attachment.metadata
		}
		rewritten, err := json.Marshal(rewrittenBlock)
		if err != nil {
			return nil, fmt.Errorf("encode attachment block %d: %w", ordinal, err)
		}
		out = append(out, rewritten)
	}
	rewritten, err := json.Marshal(out)
	if err != nil {
		return nil, fmt.Errorf("encode content blocks: %w", err)
	}
	return rewritten, nil
}
