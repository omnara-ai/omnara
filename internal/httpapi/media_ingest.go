package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
)

const (
	maxAttachmentBytes         = 10 * 1024 * 1024
	maxAttachmentsPerInput     = 20
	maxAttachmentFilenameBytes = 255
	maxTotalAttachmentBytes    = int(modelcontext.MaxResolvedMediaBytes)
	maxContentBlockBytes       = 1024 * 1024
)

type mediaIngestError struct {
	message string
}

func (e mediaIngestError) Error() string { return e.message }

type mediaIngestContext struct {
	ProjectID      storage.ID
	AgentID        storage.ID
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
	ctx context.Context,
	ingest mediaIngestContext,
	contentBlocks json.RawMessage,
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
		if len(pending) == maxAttachmentsPerInput {
			return nil, mediaIngestError{
				fmt.Sprintf("too many attachments: limit is %d per input", maxAttachmentsPerInput),
			}
		}
		if len(block.Filename) > maxAttachmentFilenameBytes {
			return nil, mediaIngestError{
				fmt.Sprintf(
					"content block %d: filename exceeds %d bytes",
					ordinal,
					maxAttachmentFilenameBytes,
				),
			}
		}
		if strings.IndexByte(block.Filename, 0) >= 0 {
			return nil, mediaIngestError{
				fmt.Sprintf("content block %d: filename must not contain U+0000", ordinal),
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
	out := make([]json.RawMessage, 0, len(rawBlocks))
	for ordinal, raw := range rawBlocks {
		attachment, ok := pending[ordinal]
		if !ok {
			out = append(out, raw)
			continue
		}
		artifactIdempotencyKey := ""
		if ingest.IdempotencyKey != "" {
			artifactIdempotencyKey = fmt.Sprintf("upload:%s:%d", ingest.IdempotencyKey, ordinal)
		}
		artifact, err := s.store.Artifacts().CreateArtifact(ctx, artifactstore.CreateArtifactInput{
			ProjectID:      ingest.ProjectID,
			AgentID:        ingest.AgentID,
			ContentType:    attachment.mediaType,
			Filename:       attachment.filename,
			Content:        attachment.content,
			MaxBytes:       maxAttachmentBytes,
			IdempotencyKey: artifactIdempotencyKey,
		})
		if err != nil {
			return nil, fmt.Errorf("store attachment %d: %w", ordinal, err)
		}
		rewrittenBlock := map[string]any{
			"type":        "media_ref",
			"artifact_id": artifact.ID.String(),
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
