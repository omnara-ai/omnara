package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/integration/slack"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/integrationstore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

var errInvalidIntegrationAction = errors.New("invalid integration action")

const integrationActionsPath = "/api/integrations/slack/actions"

func (s *Server) integrationActionsRoute(w http.ResponseWriter, r *http.Request) {
	raw, ok := readIntegrationCallbackBody(w, r, slack.ActionBodyMaxBytes)
	if !ok {
		return
	}
	envelope, err := slack.DecodeActionsEnvelope(raw)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	install, ok := s.verifySignedSlackCallback(w, r, raw, envelope.APIAppID, envelope.Team.ID)
	if !ok {
		return
	}
	installIdentity, err := slack.ParseInstallIdentity(install.ProviderIdentity)
	if err != nil {
		writeIntegrationProviderError(w, err)
		return
	}
	if !slack.ValidateActionIdentity(
		slack.Identity{
			AppID:       install.ProviderAccountRef,
			WorkspaceID: install.ProviderTenantID,
			BotUserID:   installIdentity.BotUserID,
		},
		envelope,
	) {
		apierror.Write(w, openapi.ErrorCodeForbidden, "invalid slack action identity")
		return
	}
	if install.State != integrationstore.IntegrationInstallStateActive {
		writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
		return
	}
	result, err := s.resolveIntegrationInteractionAction(r, install, envelope)
	if err != nil {
		if errors.Is(err, storeerr.ErrNotFound) || errors.Is(err, storeerr.ErrUnauthorized) ||
			errors.Is(err, errInvalidIntegrationAction) {
			writeJSON(w, http.StatusOK, map[string]string{"ok": "ignored"})
			return
		}
		writeIntegrationProviderError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

func (s *Server) resolveIntegrationInteractionAction(
	r *http.Request,
	install integrationstore.IntegrationInstallRecord,
	envelope slack.ActionsEnvelope,
) (map[string]any, error) {
	actionValue, err := slack.PromptActionFromActions(envelope)
	if err != nil {
		return nil, fmt.Errorf("%w: %w", errInvalidIntegrationAction, err)
	}
	agentID, err := publicid.Decode(publicid.KindAgent, actionValue.AgentID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid agent id", errInvalidIntegrationAction)
	}
	interactionID, err := publicid.Decode(publicid.KindAgentInteraction, actionValue.InteractionID)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid interaction id", errInvalidIntegrationAction)
	}
	integrationTargetID, err := publicid.Decode(
		publicid.KindIntegrationTarget,
		actionValue.IntegrationTargetID,
	)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid integration target id", errInvalidIntegrationAction)
	}
	integrationTarget, err := s.store.Integrations().GetIntegrationTarget(
		r.Context(),
		install.ProjectID,
		integrationTargetID,
	)
	if err != nil {
		return nil, err
	}
	if integrationTarget.IntegrationInstallID != install.ID {
		return nil, storeerr.ErrUnauthorized
	}
	binding, err := s.store.Integrations().GetActiveReceiveBindingForTarget(
		r.Context(),
		install.ProjectID,
		agentID,
		integrationTargetID,
	)
	if err != nil {
		return nil, err
	}
	if binding.IntegrationInstallID != install.ID {
		return nil, storeerr.ErrUnauthorized
	}
	existing, found, err := s.store.Execution().GetAgentInteraction(
		r.Context(),
		install.ProjectID,
		agentID,
		interactionID,
	)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, storeerr.ErrNotFound
	}
	if existing.State != executionstore.AgentInteractionStateOpen {
		return map[string]any{
			"ok":   "already_resolved",
			"text": "This prompt has already been resolved.",
		}, nil
	}
	resolutionResult, err := integrationInteractionResolution(existing, envelope)
	if err != nil {
		return nil, err
	}
	if resolutionResult.InvalidReason != "" {
		return map[string]any{"ok": "invalid", "text": resolutionResult.InvalidReason}, nil
	}
	resolution := resolutionResult.Resolution
	displayName := ""
	if names, err := s.store.Execution().ListActorDisplayNames(
		r.Context(),
		install.ProjectID,
		install.Provider,
		install.ProviderTenantID,
		[]string{envelope.User.ID},
	); err == nil {
		displayName = names[envelope.User.ID]
	}
	if displayName == "" {
		displayName = envelope.User.DisplayName()
	}
	if _, err := s.store.Execution().ResolveAgentInteraction(r.Context(), executionstore.ResolveAgentInteractionInput{
		ProjectID:  install.ProjectID,
		AgentID:    agentID,
		ID:         interactionID,
		Resolution: resolution,
		Actor: &executionstore.ActorParams{
			Provider:         install.Provider,
			ProviderTenantID: install.ProviderTenantID,
			ProviderUserID:   envelope.User.ID,
			DisplayName:      &displayName,
		},
		IntegrationTargetID:        integrationTargetID,
		IntegrationTargetBindingID: binding.ID,
		IntegrationInstallID:       install.ID,
	}); err != nil {
		if errors.Is(err, storeerr.ErrIdempotencyConflict) {
			return map[string]any{
				"ok":   "already_resolved",
				"text": "This prompt has already been resolved.",
			}, nil
		}
		return nil, err
	}
	text := integrationActionResolvedText(existing, resolution)
	s.replaceSlackActionMessageAsync(r.Context(), envelope.ResponseURL, text)
	return map[string]any{"ok": "resolved", "text": text}, nil
}

func (s *Server) replaceSlackActionMessageAsync(ctx context.Context, responseURL string, text string) {
	if strings.TrimSpace(responseURL) == "" {
		return
	}
	go s.replaceSlackActionMessage(context.WithoutCancel(ctx), responseURL, text)
}

func (s *Server) replaceSlackActionMessage(ctx context.Context, responseURL string, text string) {
	if strings.TrimSpace(responseURL) == "" {
		return
	}
	result, err := slack.ReplaceOriginalActionMessage(ctx, s.slackOAuth.HTTPClient, responseURL, text)
	if err != nil {
		s.log.Warn("slack action message update failed", "error", err)
		return
	}
	if result.RateLimited || result.TransientFailure || result.PermanentFailure || result.DeliveryUnknown {
		s.log.Warn("slack action message update failed", "message", result.Message)
	}
}

func integrationActionResolvedText(
	existing executionstore.AgentInteractionRecord,
	resolution interactionform.Resolution,
) string {
	switch existing.InteractionKind {
	case executionstore.AgentInteractionKindPermission:
		request, err := toolpermission.ParseRequest(existing.Request)
		if err != nil {
			return "Permission response recorded."
		}
		decision, err := toolpermission.Resolve(request, resolution)
		if err != nil {
			return "Permission response recorded."
		}
		text := "Permission denied"
		if decision.Decision == toolpermission.DecisionAllow {
			text = "Permission allowed"
		}
		if toolName := interactionToolName(existing.Request); toolName != "" {
			return text + " for " + toolName + "."
		}
		return text + "."
	case executionstore.AgentInteractionKindQuestion:
		return "Answers recorded."
	default:
		return "Recorded."
	}
}

func interactionToolName(request json.RawMessage) string {
	value, err := toolpermission.ParseRequest(request)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(value.Authorization.ToolName)
}

type integrationInteractionResolutionResult struct {
	Resolution    interactionform.Resolution
	InvalidReason string
}

func integrationInteractionResolution(
	existing executionstore.AgentInteractionRecord,
	envelope slack.ActionsEnvelope,
) (integrationInteractionResolutionResult, error) {
	value, err := existing.Form()
	if err != nil {
		return integrationInteractionResolutionResult{},
			fmt.Errorf("parse stored interaction form: %w", err)
	}
	responseResult := slack.ResolveInteractionForm(value, envelope.State)
	if responseResult.InvalidReason != "" {
		return integrationInteractionResolutionResult{InvalidReason: responseResult.InvalidReason}, nil
	}
	return integrationInteractionResolutionResult{Resolution: responseResult.Resolution}, nil
}
