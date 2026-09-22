package tools

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/publicid"
)

func (e Executor) sendSlackArtifacts(
	ctx context.Context,
	turn Turn,
	target slack.MessageTarget,
	input slackPostInput,
) (toolResultContent, error) {
	var slept time.Duration
	files := make([]slack.UploadedFile, 0, len(input.ArtifactIDs))
	for _, publicID := range input.ArtifactIDs {
		id, err := publicid.Decode(publicid.KindArtifact, publicID)
		if err != nil {
			return toolResultContent{}, err
		}
		content, artifact, err := e.Store.Artifacts().GetArtifactBlob(ctx, turn.ProjectID, turn.AgentID, id)
		if err != nil {
			return toolResultContent{}, fmt.Errorf("load artifact %s: %w", publicID, err)
		}
		filename := modelcontext.MediaFilename(artifact.Filename, artifact.ContentType)
		for attempt := 1; attempt <= integrationMessageSendAttempts; attempt++ {
			fileID, result, err := slack.UploadFile(
				ctx,
				e.IntegrationHTTPClient,
				target,
				filename,
				content,
				func(ctx context.Context) error { return e.ensureIntegrationPostOwnership(ctx, turn) },
			)
			if err != nil {
				return toolResultContent{}, err
			}
			if fileID != "" {
				files = append(files, slack.UploadedFile{ID: fileID, Title: filename})
				break
			}
			if result.RateLimited {
				retry, err := sleepForIntegrationRateLimit(ctx, result.RetryAfter, &slept, attempt)
				if err != nil {
					return toolResultContent{}, err
				}
				if retry {
					continue
				}
			} else if result.TransientFailure && attempt < integrationMessageSendAttempts {
				continue
			}
			return slackAppFailureContent(target.TargetRef, result)
		}
	}
	// Upload preparation must not consume the final publication retry budget.
	slept = 0
	for attempt := 1; attempt <= integrationMessageSendAttempts; attempt++ {
		if err := e.ensureIntegrationPostOwnership(ctx, turn); err != nil {
			return toolResultContent{}, err
		}
		result, err := slack.CompleteFileUploads(ctx, e.IntegrationHTTPClient, target, files, input.Text)
		if err != nil {
			return toolResultContent{}, err
		}
		if result == (slack.APIResult{}) {
			ids := make([]string, 0, len(files))
			for _, file := range files {
				ids = append(ids, file.ID)
			}
			return structuredToolResultContent(
				map[string]any{
					"app":        target.TargetRef,
					"channel_id": target.Channel,
					"thread_ts":  target.ThreadTS,
					"file_ids":   ids,
				},
			)
		}
		if result.RateLimited {
			retry, err := sleepForIntegrationRateLimit(ctx, result.RetryAfter, &slept, attempt)
			if err != nil {
				return toolResultContent{}, err
			}
			if retry {
				continue
			}
		}
		// An uncertain completion is never retried: uploads may already be posted.
		return slackAppFailureContent(target.TargetRef, result)
	}
	return toolResultContent{}, errors.New("slack file publication was not confirmed")
}
