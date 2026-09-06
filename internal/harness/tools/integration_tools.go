package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/modelcontext"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

var (
	errMissingIntegrationTarget = errors.New("missing integration target")
	errIntegrationDisabled      = errors.New("integration is disabled")
)

const (
	integrationMessageSendAttempts          = 3
	integrationMessageSendMaxRateLimitSleep = 15 * time.Second
)

func validateIntegrationMessageInput(input json.RawMessage) error {
	_, err := resolveIntegrationMessageRequest(input)
	return err
}

func validateIntegrationTargetInput(input json.RawMessage) error {
	_, err := resolveIntegrationTargetRequest(input)
	return err
}

func runIntegrationMessageAsync(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	record, err := call.Executor.Store.Execution().GetToolCall(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		call.ToolCallID,
	)
	if err != nil {
		return nil, err
	}
	content, runErr := call.Executor.dispatchIntegrationMessageSend(
		ctx,
		call.Turn,
		call.Call,
		record,
	)
	if runErr != nil {
		return failAsynchronously(content, runErr), nil
	}
	return completeAsynchronously(content), nil
}

func sleepForIntegrationRateLimit(
	ctx context.Context,
	retryAfter time.Duration,
	rateLimitSlept *time.Duration,
	attempt int,
) (bool, error) {
	if retryAfter < 0 || *rateLimitSlept+retryAfter > integrationMessageSendMaxRateLimitSleep ||
		attempt >= integrationMessageSendAttempts {
		return false, nil
	}
	timer := time.NewTimer(retryAfter)
	select {
	case <-ctx.Done():
		timer.Stop()
		return false, ctx.Err()
	case <-timer.C:
	}
	*rateLimitSlept += retryAfter
	return true, nil
}

func (e Executor) ensureIntegrationPostOwnership(ctx context.Context, turn Turn) error {
	return e.Store.Execution().EnsureRuntimeLockActive(
		ctx,
		turn.ProjectID,
		turn.AgentID,
		turn.RuntimeLockID,
	)
}

type integrationToolResult struct {
	Provider           string `json:"provider,omitempty"`
	Code               string `json:"code"`
	TargetRef          string `json:"target_ref,omitempty"`
	ProviderMessageID  string `json:"provider_message_id,omitempty"`
	Message            string `json:"message,omitempty"`
	RetryAfterSeconds  int    `json:"retry_after_seconds,omitempty"`
	RetryAfterAt       string `json:"retry_after_at,omitempty"`
	IntegrationRefKind string `json:"integration_ref_kind,omitempty"`
}

type integrationToolTarget struct {
	ID              storage.ID
	Provider        string
	PublicID        string
	TargetRef       string
	ProviderRefKind string
	ProviderRef     string
	APIToken        string
}

func (e Executor) PostIntegrationRuntimeMessage(ctx context.Context, turn Turn, text string) error {
	target, err := e.currentIntegrationToolTarget(ctx, turn)
	if err != nil {
		if errors.Is(err, errMissingIntegrationTarget) || errors.Is(err, errIntegrationDisabled) {
			return nil
		}
		return err
	}
	slackTarget, err := slackMessageTarget(target)
	if err != nil {
		return err
	}
	agentPublicID, err := publicid.Encode(publicid.KindAgent, turn.AgentID)
	if err != nil {
		return err
	}
	payload := map[string]any{
		"channel": slackTarget.Channel,
		"text":    text,
		"metadata": map[string]any{
			"event_type": slack.MessageMarkerEventType,
			"event_payload": map[string]any{
				"agent_id":         agentPublicID,
				"provider_call_id": "runtime_error:" + turn.RuntimeLockID.String(),
				"target_ref":       target.TargetRef,
			},
		},
	}
	if slackTarget.ThreadTS != "" {
		payload["thread_ts"] = slackTarget.ThreadTS
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	return e.postIntegrationPayloadToTarget(
		ctx,
		turn,
		target,
		body,
		"integration runtime message could not be delivered",
	)
}

func (e Executor) dispatchIntegrationMessageSend(
	ctx context.Context,
	turn Turn,
	call model.ToolCall,
	record executionstore.ToolCallRecord,
) (toolResultContent, error) {
	input, err := resolveIntegrationMessageRequest(call.Input)
	if err != nil {
		return toolResultContent{}, err
	}
	target, targetErr := e.currentIntegrationToolTarget(ctx, turn)
	if targetErr != nil {
		result, resultErr := integrationTargetFailureResult(target.TargetRef, targetErr)
		if resultErr != nil {
			return toolResultContent{}, resultErr
		}
		return result, targetErr
	}
	slackTarget, err := slackMessageTarget(target)
	if err != nil {
		return toolResultContent{}, err
	}
	if len(input.ArtifactIDs) != 0 {
		return e.dispatchIntegrationArtifactSend(ctx, turn, slackTarget, input)
	}

	agentPublicID, err := publicid.Encode(publicid.KindAgent, turn.AgentID)
	if err != nil {
		return toolResultContent{}, err
	}
	var rateLimitSlept time.Duration
	for attempt := 1; attempt <= integrationMessageSendAttempts; attempt++ {
		if err := e.ensureIntegrationPostOwnership(ctx, turn); err != nil {
			return toolResultContent{}, err
		}
		posted, err := slack.PostMessage(
			ctx,
			e.IntegrationHTTPClient,
			slackTarget,
			agentPublicID,
			record.ProviderCallID,
			input.Text,
		)
		if err != nil {
			return toolResultContent{}, err
		}
		switch {
		case posted.MessageID != "":
			return e.integrationDelivered(target.TargetRef, posted.MessageID)
		case posted.RateLimited:
			if slept, err := sleepForIntegrationRateLimit(ctx, posted.RetryAfter, &rateLimitSlept, attempt); err != nil {
				return toolResultContent{}, err
			} else if slept {
				continue
			}
			return e.integrationRateLimited(target.TargetRef, posted.RetryAfter)
		case posted.DeliveryUnknown, posted.TransientFailure:
			if result, handled, err := e.integrationReadbackResult(
				ctx,
				target,
				slackTarget,
				agentPublicID,
				record.ProviderCallID,
				record.CreatedAt,
			); handled {
				return result, err
			}
			if attempt == integrationMessageSendAttempts {
				return e.integrationDeliveryUnknown(target.TargetRef, errors.New(posted.Message))
			}
		default:
			return integrationSlackFailureResult(target.TargetRef, posted)
		}
	}
	return e.integrationDeliveryUnknown(
		target.TargetRef,
		errors.New("message delivery could not be confirmed"),
	)
}

func (e Executor) dispatchIntegrationArtifactSend(
	ctx context.Context,
	turn Turn,
	slackTarget slack.MessageTarget,
	input integrationMessageRequest,
) (toolResultContent, error) {
	var rateLimitSlept time.Duration
	uploadedFiles := make([]slack.UploadedFile, 0, len(input.ArtifactIDs))
	for _, artifactPublicID := range input.ArtifactIDs {
		artifactID, err := publicid.Decode(publicid.KindArtifact, artifactPublicID)
		if err != nil {
			return toolResultContent{}, err
		}
		content, artifact, err := e.Store.Artifacts().GetArtifactBlob(
			ctx,
			turn.ProjectID,
			turn.AgentID,
			artifactID,
		)
		if err != nil {
			return toolResultContent{}, fmt.Errorf("load artifact %s: %w", artifactPublicID, err)
		}
		filename := modelcontext.MediaFilename(artifact.Filename, artifact.ContentType)
		var fileID string
		for attempt := 1; attempt <= integrationMessageSendAttempts; attempt++ {
			var result slack.APIResult
			fileID, result, err = slack.UploadFile(
				ctx,
				e.IntegrationHTTPClient,
				slackTarget,
				filename,
				content,
				func(ctx context.Context) error {
					return e.ensureIntegrationPostOwnership(ctx, turn)
				},
			)
			if err != nil {
				return toolResultContent{}, err
			}
			if fileID != "" {
				break
			}
			switch {
			case result.RateLimited:
				if slept, err := sleepForIntegrationRateLimit(ctx, result.RetryAfter, &rateLimitSlept, attempt); err != nil {
					return toolResultContent{}, err
				} else if slept {
					continue
				}
				return e.integrationRateLimited(slackTarget.TargetRef, result.RetryAfter)
			case result.TransientFailure && attempt < integrationMessageSendAttempts:
				continue
			default:
				return integrationSlackFailureResult(slackTarget.TargetRef, result)
			}
		}
		uploadedFiles = append(uploadedFiles, slack.UploadedFile{ID: fileID, Title: filename})
	}
	rateLimitSlept = 0
	for attempt := 1; attempt <= integrationMessageSendAttempts; attempt++ {
		if err := e.ensureIntegrationPostOwnership(ctx, turn); err != nil {
			return toolResultContent{}, err
		}
		result, err := slack.CompleteFileUploads(
			ctx,
			e.IntegrationHTTPClient,
			slackTarget,
			uploadedFiles,
			input.Text,
		)
		if err != nil {
			return toolResultContent{}, err
		}
		switch {
		case result == (slack.APIResult{}):
			return e.integrationDelivered(slackTarget.TargetRef, "")
		case result.RateLimited:
			if slept, err := sleepForIntegrationRateLimit(ctx, result.RetryAfter, &rateLimitSlept, attempt); err != nil {
				return toolResultContent{}, err
			} else if slept {
				continue
			}
			return e.integrationRateLimited(slackTarget.TargetRef, result.RetryAfter)
		case result.DeliveryUnknown:
			message := result.Message
			if message == "" {
				message = "file delivery could not be confirmed"
			}
			return e.integrationDeliveryUnknown(slackTarget.TargetRef, errors.New(message))
		default:
			return integrationSlackFailureResult(slackTarget.TargetRef, result)
		}
	}
	return e.integrationDeliveryUnknown(
		slackTarget.TargetRef,
		errors.New("file delivery could not be confirmed"),
	)
}

func integrationSlackFailureResult(
	targetRef string,
	result slack.APIResult,
) (toolResultContent, error) {
	code := result.Code
	if code == "" {
		code = "permanent_failure"
	}
	message := result.Message
	if message == "" {
		message = code
	}
	content, err := structuredToolResultContent(
		integrationToolResult{
			Provider:  integrationstore.IntegrationProviderSlack,
			Code:      code,
			TargetRef: targetRef,
			Message:   message,
		},
	)
	if err != nil {
		return toolResultContent{}, err
	}
	return content, errors.New(message)
}

func integrationTargetFailureResult(
	targetRef string,
	cause error,
) (toolResultContent, error) {
	code := "integration_error"
	message := cause.Error()
	if errors.Is(cause, errIntegrationDisabled) {
		code = "integration_disabled"
	} else if errors.Is(cause, errMissingIntegrationTarget) {
		code = "missing_integration_target"
	}
	return structuredToolResultContent(integrationToolResult{
		Provider:  integrationstore.IntegrationProviderSlack,
		Code:      code,
		TargetRef: targetRef,
		Message:   message,
	})
}

func (e Executor) integrationReadbackResult(
	ctx context.Context,
	target integrationToolTarget,
	slackTarget slack.MessageTarget,
	agentPublicID, providerCallID string,
	since time.Time,
) (toolResultContent, bool, error) {
	delivered, found, readback, err := slack.ReconcileMessage(
		ctx,
		e.IntegrationHTTPClient,
		slackTarget,
		agentPublicID,
		providerCallID,
		since,
	)
	if err != nil {
		result, resultErr := e.integrationDeliveryUnknown(target.TargetRef, err)
		return result, true, resultErr
	}
	if readback.RateLimited || readback.TransientFailure || readback.PermanentFailure ||
		readback.DeliveryUnknown {
		result, resultErr := e.integrationDeliveryUnknown(
			target.TargetRef,
			errors.New(readback.Message),
		)
		return result, true, resultErr
	}
	if found {
		result, resultErr := e.integrationDelivered(target.TargetRef, delivered)
		return result, true, resultErr
	}
	return toolResultContent{}, false, nil
}

func setIntegrationTarget(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	input, err := resolveIntegrationTargetRequest(call.Call.Input)
	if err != nil {
		return nil, err
	}
	authorizationInput, err := marshalJSON(input)
	if err != nil {
		return nil, fmt.Errorf("marshal integration target authorization: %w", err)
	}
	if err := authorizeToolExecution(
		ctx,
		call.Reader,
		call.Turn,
		call.Call,
		authorizationInput,
	); err != nil {
		return nil, err
	}
	targetRef := input.TargetRef
	targets, err := call.Reader.ListIntegrationTargets(ctx)
	if err != nil {
		return nil, err
	}
	var selected integrationstore.IntegrationTargetSummary
	for _, target := range targets {
		if target.TargetRef == targetRef {
			selected = target
			break
		}
	}
	if selected.ID == storage.NilID ||
		selected.InstallState != integrationstore.IntegrationInstallStateActive {
		content, resultErr := structuredToolResultContent(
			integrationToolResult{
				Code:      "missing_integration_target",
				TargetRef: targetRef,
				Message:   "integration target is not attached to this agent or is not active",
			},
		)
		if resultErr != nil {
			return nil, resultErr
		}
		return failInTransaction(
			content,
			errors.New(
				"integration target is not attached to this agent or is not active",
			),
		), nil
	}
	content, err := structuredToolResultContent(
		integrationToolResult{
			Provider:           selected.Provider,
			Code:               "target_set",
			TargetRef:          targetRef,
			IntegrationRefKind: selected.ProviderRefKind,
		},
	)
	if err != nil {
		return nil, err
	}
	completion, err := successfulToolCallCompletion(content)
	if err != nil {
		return nil, err
	}
	return executeInTransaction(
		executionstore.SetIntegrationTargetForToolCall(selected.ID, completion),
		func(err error) (transactionalPhaseResult, error) {
			if !errors.Is(err, storeerr.ErrConflict) {
				return nil, err
			}
			content, resultErr := structuredToolResultContent(
				integrationToolResult{
					Code:      "missing_integration_target",
					TargetRef: targetRef,
					Message:   err.Error(),
				},
			)
			if resultErr != nil {
				return nil, resultErr
			}
			return failInTransaction(content, err), nil
		},
	), nil
}

func (e Executor) currentIntegrationToolTarget(
	ctx context.Context,
	turn Turn,
) (integrationToolTarget, error) {
	current, err := e.currentIntegrationTargetIdentity(ctx, turn)
	if err != nil {
		return current, err
	}
	return e.integrationToolTargetByID(ctx, turn.ProjectID, turn.AgentID, current.ID)
}

func (e Executor) currentIntegrationTargetIdentity(
	ctx context.Context,
	turn Turn,
) (integrationToolTarget, error) {
	targets, err := e.Store.Integrations().ListIntegrationTargets(ctx, turn.ProjectID, turn.AgentID)
	if err != nil {
		return integrationToolTarget{}, err
	}
	var current integrationstore.IntegrationTargetSummary
	for _, target := range targets {
		if target.IsCurrent {
			current = target
			break
		}
	}
	if current.ID == storage.NilID {
		return integrationToolTarget{}, errMissingIntegrationTarget
	}
	target := integrationToolTarget{
		ID:              current.ID,
		Provider:        current.Provider,
		TargetRef:       current.TargetRef,
		ProviderRefKind: current.ProviderRefKind,
		ProviderRef:     current.ProviderRef,
	}
	if current.InstallState != integrationstore.IntegrationInstallStateActive {
		return target, errIntegrationDisabled
	}
	return target, nil
}

func (e Executor) integrationToolTargetByID(
	ctx context.Context,
	projectID, agentID, targetID storage.ID,
) (integrationToolTarget, error) {
	target, err := e.Store.Integrations().GetIntegrationTarget(ctx, projectID, targetID)
	if err != nil {
		return integrationToolTarget{}, err
	}
	if target.AgentID != agentID {
		return integrationToolTarget{}, errMissingIntegrationTarget
	}
	publicID, err := publicid.Encode(publicid.KindIntegrationTarget, target.ID)
	if err != nil {
		return integrationToolTarget{}, err
	}
	install, err := e.Store.Integrations().GetIntegrationInstall(ctx, projectID, target.IntegrationInstallID)
	if err != nil {
		return integrationToolTarget{}, err
	}
	if install.Provider != integrationstore.IntegrationProviderSlack {
		return integrationToolTarget{}, fmt.Errorf(
			"unsupported integration provider %q",
			install.Provider,
		)
	}
	if install.State != integrationstore.IntegrationInstallStateActive {
		return integrationToolTarget{
			ID:        target.ID,
			PublicID:  publicID,
			TargetRef: target.TargetRef,
		}, errIntegrationDisabled
	}
	payload, err := e.Store.Secrets().GetProjectOwnedSecretPayload(
		ctx,
		install.OrgID,
		install.ProjectID,
		install.CredentialSecretID,
	)
	if err != nil {
		return integrationToolTarget{}, err
	}
	credentials, err := slack.AppCredentialsFromPayload(payload)
	if err != nil {
		return integrationToolTarget{
			ID:        target.ID,
			PublicID:  publicID,
			TargetRef: target.TargetRef,
		}, errIntegrationDisabled
	}
	return integrationToolTarget{
		ID:              target.ID,
		Provider:        install.Provider,
		PublicID:        publicID,
		TargetRef:       target.TargetRef,
		ProviderRefKind: target.ProviderRefKind,
		ProviderRef:     target.ProviderRef,
		APIToken:        credentials.BotToken,
	}, nil
}

func slackMessageTarget(target integrationToolTarget) (slack.MessageTarget, error) {
	if target.Provider != integrationstore.IntegrationProviderSlack {
		return slack.MessageTarget{}, fmt.Errorf(
			"unsupported integration provider %q",
			target.Provider,
		)
	}
	channel, threadTS, err := slack.Destination(target.ProviderRefKind, target.ProviderRef)
	if err != nil {
		return slack.MessageTarget{}, err
	}
	return slack.MessageTarget{
		TargetRef: target.TargetRef,
		Channel:   channel,
		ThreadTS:  threadTS,
		BotToken:  target.APIToken,
	}, nil
}

func (e Executor) integrationDelivered(
	target, providerMessageID string,
) (toolResultContent, error) {
	return structuredToolResultContent(
		integrationToolResult{
			Provider:          integrationstore.IntegrationProviderSlack,
			Code:              "delivered",
			TargetRef:         target,
			ProviderMessageID: providerMessageID,
		},
	)
}

func (e Executor) integrationRateLimited(
	target string,
	retryAfter time.Duration,
) (toolResultContent, error) {
	seconds := int(retryAfter.Seconds())
	result := integrationToolResult{
		Provider:          integrationstore.IntegrationProviderSlack,
		Code:              "rate_limited",
		TargetRef:         target,
		Message:           "integration provider rate limited the request",
		RetryAfterSeconds: seconds,
	}
	if retryAfter > 0 {
		result.RetryAfterAt = e.now().Add(retryAfter).UTC().Format(time.RFC3339)
	}
	content, err := structuredToolResultContent(result)
	if err != nil {
		return toolResultContent{}, err
	}
	return content, errors.New("integration provider rate limited the request")
}

func (e Executor) integrationDeliveryUnknown(
	target string,
	cause error,
) (toolResultContent, error) {
	message := "integration delivery outcome is unknown"
	if cause != nil && cause.Error() != "" {
		message += ": " + cause.Error()
	}
	content, err := structuredToolResultContent(
		integrationToolResult{
			Provider:  integrationstore.IntegrationProviderSlack,
			Code:      "delivery_unknown",
			TargetRef: target,
			Message:   message,
		},
	)
	if err != nil {
		return toolResultContent{}, err
	}
	return content, errors.New(message)
}
