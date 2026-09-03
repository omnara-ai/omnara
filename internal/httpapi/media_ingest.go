package httpapi

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/dbsafe"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
)

const (
	maxAttachmentBytes         = 10 * 1024 * 1024
	maxAttachmentsPerInput     = 20
	maxContentBlocksPerInput   = 100
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
	ProjectID            storage.ID
	AgentID              storage.ID
	IntegrationInstallID storage.ID
	IdempotencyKey       string
	RuntimeLease         *integrationstore.IntegrationRuntimeLeaseProof
}

type pendingAttachment struct {
	ordinal   int
	mediaType string
	filename  string
	content   []byte
	metadata  json.RawMessage
}

type inlineMediaPlan struct {
	contentBlocks json.RawMessage
	rawBlocks     []json.RawMessage
	attachments   map[int]pendingAttachment
	validated     bool
}

type inlineMediaOwner uint8

const (
	inlineMediaAgentInput inlineMediaOwner = iota + 1
	inlineMediaToolResult
)

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
	owner inlineMediaOwner,
) (json.RawMessage, error) {
	plan, err := preflightInlineMedia(contentBlocks, owner, 0)
	if err != nil {
		return nil, err
	}
	return s.materializeInlineMedia(ctx, ingest, plan)
}

// preflightInlineMedia validates and decodes the complete input without making
// any durable writes. The decoded bytes are retained in the plan so channel
// fanout does not decode large attachments once per recipient.
func preflightInlineMedia(
	contentBlocks json.RawMessage,
	owner inlineMediaOwner,
	maxBlocks int,
) (inlineMediaPlan, error) {
	var rawBlocks []json.RawMessage
	if err := json.Unmarshal(contentBlocks, &rawBlocks); err != nil {
		return inlineMediaPlan{}, mediaIngestError{"content blocks must be a JSON array"}
	}
	if rawBlocks == nil {
		return inlineMediaPlan{}, mediaIngestError{"content blocks must be a JSON array"}
	}
	if len(rawBlocks) == 0 && owner == inlineMediaAgentInput {
		return inlineMediaPlan{}, mediaIngestError{
			"content blocks must contain at least one block",
		}
	}
	if maxBlocks > 0 && len(rawBlocks) > maxBlocks {
		return inlineMediaPlan{}, mediaIngestError{
			fmt.Sprintf("too many content blocks: limit is %d per input", maxBlocks),
		}
	}
	nonAttachmentBytes := 0
	pending := make(map[int]pendingAttachment)
	canonicalBlocks := make([]json.RawMessage, 0, len(rawBlocks))
	totalBytes := 0
	for ordinal, raw := range rawBlocks {
		var fields map[string]json.RawMessage
		if err := json.Unmarshal(raw, &fields); err != nil || fields == nil {
			canonicalBlocks = append(canonicalBlocks, raw)
			nonAttachmentBytes += len(raw)
			continue
		}
		var block inboundBlock
		if err := json.Unmarshal(raw, &block); err != nil {
			canonicalBlocks = append(canonicalBlocks, raw)
			nonAttachmentBytes += len(raw)
			continue
		}
		if block.Type == "media_ref" {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf(
					"content block %d: media_ref blocks are output-only; attach media as a base64 `media` block",
					ordinal,
				),
			}
		}
		if block.Type != "media" {
			canonicalBlocks = append(canonicalBlocks, raw)
			nonAttachmentBytes += len(raw)
			continue
		}
		for field := range fields {
			switch field {
			case "type", "data", "media_type", "filename", "metadata":
			default:
				return inlineMediaPlan{}, mediaIngestError{
					fmt.Sprintf("content block %d: unsupported field %q", ordinal, field),
				}
			}
		}
		if len(block.Metadata) > 0 {
			var metadata map[string]json.RawMessage
			if err := json.Unmarshal(block.Metadata, &metadata); err != nil || metadata == nil {
				return inlineMediaPlan{}, mediaIngestError{
					fmt.Sprintf("content block %d: metadata must be a JSON object", ordinal),
				}
			}
		}
		nonAttachmentBytes += len(block.Metadata)
		if block.Data == "" {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf(
					"content block %d: media block requires non-empty base64 data",
					ordinal,
				),
			}
		}
		if !modelcontext.IsAttachmentMedia(block.MediaType) {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf(
					"content block %d: unsupported media_type %q",
					ordinal,
					block.MediaType,
				),
			}
		}
		// DecodedLen can overestimate by at most two padding bytes. Reject
		// obviously oversized input before allocating its decoded buffer, then
		// enforce the exact limits after decoding.
		decodedUpperBound := base64.StdEncoding.DecodedLen(len(block.Data))
		if decodedUpperBound > maxAttachmentBytes+2 ||
			decodedUpperBound > maxTotalAttachmentBytes-totalBytes+2 {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf("content block %d: attachment exceeds its size limit", ordinal),
			}
		}
		content, err := base64.StdEncoding.DecodeString(block.Data)
		if err != nil {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf("content block %d: data must be standard base64", ordinal),
			}
		}
		if len(content) == 0 {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf("content block %d: data must not be empty", ordinal),
			}
		}
		if len(content) > maxAttachmentBytes {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf(
					"content block %d: attachment exceeds %d bytes",
					ordinal,
					maxAttachmentBytes,
				),
			}
		}
		if modelcontext.IsTextDocumentMediaType(block.MediaType) && !utf8.Valid(content) {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf("content block %d: text attachment must be valid UTF-8", ordinal),
			}
		}
		if len(pending) == maxAttachmentsPerInput {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf("too many attachments: limit is %d per input", maxAttachmentsPerInput),
			}
		}
		if utf8.RuneCountInString(block.Filename) > maxAttachmentFilenameRunes {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf(
					"content block %d: filename exceeds %d characters",
					ordinal,
					maxAttachmentFilenameRunes,
				),
			}
		}
		if err := dbsafe.Text(block.Filename); err != nil {
			return inlineMediaPlan{}, mediaIngestError{
				fmt.Sprintf("content block %d: filename %s", ordinal, err),
			}
		}
		totalBytes += len(content)
		if totalBytes > maxTotalAttachmentBytes {
			return inlineMediaPlan{}, mediaIngestError{
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
		placeholder := map[string]any{
			"type":        "media_ref",
			"artifact_id": "00000000-0000-0000-0000-000000000001",
		}
		if len(block.Metadata) > 0 {
			placeholder["metadata"] = block.Metadata
		}
		canonical, err := json.Marshal(placeholder)
		if err != nil {
			return inlineMediaPlan{}, fmt.Errorf("encode content block %d preflight: %w", ordinal, err)
		}
		canonicalBlocks = append(canonicalBlocks, canonical)
	}
	if nonAttachmentBytes > maxContentBlockBytes {
		return inlineMediaPlan{}, mediaIngestError{
			fmt.Sprintf(
				"content blocks exceed %d bytes excluding attachments",
				maxContentBlockBytes,
			),
		}
	}
	canonical, err := json.Marshal(canonicalBlocks)
	if err != nil {
		return inlineMediaPlan{}, fmt.Errorf("encode content preflight: %w", err)
	}
	var validationErr error
	switch owner {
	case inlineMediaAgentInput:
		validationErr = executionstore.ValidateAgentInputContentBlocks(canonical)
	case inlineMediaToolResult:
		validationErr = executionstore.ValidateToolResultContentBlocks(canonical)
	default:
		return inlineMediaPlan{}, errors.New("inline media owner is invalid")
	}
	if validationErr != nil {
		return inlineMediaPlan{}, mediaIngestError{validationErr.Error()}
	}
	return inlineMediaPlan{
		contentBlocks: contentBlocks,
		rawBlocks:     rawBlocks,
		attachments:   pending,
		validated:     true,
	}, nil
}

func (s *Server) materializeInlineMedia(
	ctx context.Context,
	ingest mediaIngestContext,
	plan inlineMediaPlan,
) (json.RawMessage, error) {
	if !plan.validated {
		return nil, errors.New("inline media plan was not preflighted")
	}
	if len(plan.attachments) == 0 {
		return plan.contentBlocks, nil
	}
	out := make([]json.RawMessage, 0, len(plan.rawBlocks))
	for ordinal, raw := range plan.rawBlocks {
		attachment, ok := plan.attachments[ordinal]
		if !ok {
			out = append(out, raw)
			continue
		}
		artifactIdempotencyKey := ""
		if ingest.IdempotencyKey != "" {
			artifactIdempotencyKey = fmt.Sprintf("upload:%s:%d", ingest.IdempotencyKey, ordinal)
		}
		artifactInput := artifactstore.CreateArtifactInput{
			ProjectID:      ingest.ProjectID,
			AgentID:        ingest.AgentID,
			ContentType:    attachment.mediaType,
			Filename:       attachment.filename,
			Content:        attachment.content,
			MaxBytes:       maxAttachmentBytes,
			IdempotencyKey: artifactIdempotencyKey,
		}
		var artifact artifactstore.ArtifactRecord
		var err error
		if ingest.RuntimeLease == nil {
			artifact, err = s.store.Artifacts().CreateArtifact(ctx, artifactInput)
		} else {
			artifact, err = s.store.Artifacts().CreateArtifactWithIntegrationRuntimeLease(
				ctx,
				artifactInput,
				ingest.IntegrationInstallID,
				ingest.RuntimeLease,
			)
		}
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
