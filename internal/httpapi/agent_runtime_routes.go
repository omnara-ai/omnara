package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
)

const (
	defaultTurnPageLimit  = executionstore.DefaultAgentTurnsReadPageLimit
	maxTurnPageLimit      = executionstore.MaxAgentTurnsReadPageLimit
	defaultEventPageLimit = executionstore.DefaultAgentEventsReadPageLimit
	maxEventPageLimit     = executionstore.MaxAgentEventsReadPageLimit
)

func (s strictOpenAPIServer) CancelAgent(
	ctx context.Context,
	request openapi.CancelAgentRequestObject,
) (openapi.CancelAgentResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.cancelAgent(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) cancelAgent(
	ctx context.Context,
	request openapi.CancelAgentRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.CancelAgentResponseObject, error) {
	principal, ok := principalFromContext(ctx)
	if !ok || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "unauthorized")
	}
	var actorParams *openapi.ExternalActorParams
	if request.Body != nil {
		actorParams = request.Body.Actor
	}
	actor, err := requestActorParams(project, principal, actorParams)
	if err != nil {
		return nil, err
	}
	cancelResult, err := s.server.store.Execution().CancelAgent(ctx, executionstore.CancelAgentInput{
		ProjectID: project.ID,
		AgentID:   agent.ID,
		Actor:     actor,
	})
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	response := openapi.CancelAgentResponse{
		Affected:               cancelResult.Affected,
		Event:                  nullableFromPtr[openapi.AgentEvent](nil),
		RuntimeCancelRequested: cancelResult.RuntimeCancelRequested,
	}
	if cancelResult.ActorID != storage.NilID {
		actorID, err := publicID(publicid.KindActor, cancelResult.ActorID)
		if err != nil {
			return nil, err
		}
		response.ActorId = &actorID
	}
	if cancelResult.Event.ID == storage.NilID {
		return openapi.CancelAgent200JSONResponse(response), nil
	}
	records, err := s.server.store.Execution().ListAgentEventsForRead(
		ctx,
		project.ID,
		agent.ID,
		cancelResult.Event.Sequence-1,
		1,
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	if len(records) != 1 {
		return nil, errors.New("cancel event not readable")
	}
	eventResponse, err := publicEventResponseFromReadRecord(records[0])
	if err != nil {
		return nil, err
	}
	response.Event = nullableFromValue(eventResponse)
	return openapi.CancelAgent200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ArchiveAgent(
	ctx context.Context,
	request openapi.ArchiveAgentRequestObject,
) (openapi.ArchiveAgentResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.archiveAgent(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) archiveAgent(
	ctx context.Context,
	_ openapi.ArchiveAgentRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.ArchiveAgentResponseObject, error) {
	principal, ok := principalFromContext(ctx)
	if !ok || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "unauthorized")
	}
	archived, machines, err := s.server.store.Execution().ArchiveAgent(ctx, project.ID, agent.ID, principal)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	s.server.startPoolMachineDeletion(ctx, machines)
	response, err := currentAgentEnvelope(archived)
	if err != nil {
		return nil, err
	}
	return openapi.ArchiveAgent200JSONResponse(response), nil
}

func (s strictOpenAPIServer) ListQueuedBacklogInputs(
	ctx context.Context,
	request openapi.ListQueuedBacklogInputsRequestObject,
) (openapi.ListQueuedBacklogInputsResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listQueuedBacklogInputs(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) listQueuedBacklogInputs(
	ctx context.Context,
	request openapi.ListQueuedBacklogInputsRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.ListQueuedBacklogInputsResponseObject, error) {
	limit, after, err := parseAgentInputQueuePageParams(request.Params.Limit, request.Params.Cursor)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	page, err := s.server.store.Execution().ListQueuedBacklogInputs(ctx, executionstore.ListQueuedBacklogInputsInput{
		ProjectID: project.ID,
		AgentID:   agent.ID,
		Limit:     limit,
		After:     after,
	})
	if err != nil {
		return nil, agentInputCommandAPIError(err)
	}
	response, err := publicAgentInputResponsesFromRecords(page.Inputs)
	if err != nil {
		return nil, err
	}
	var nextCursor *string
	if len(page.Inputs) > 0 {
		nextCursor, err = encodeNextAgentInputQueueCursor(page.HasMore, page.Inputs[len(page.Inputs)-1])
		if err != nil {
			return nil, err
		}
	}
	return openapi.ListQueuedBacklogInputs200JSONResponse(
		openapi.ListAgentInputsResponse{Data: response, NextCursor: nullableFromPtr(nextCursor)},
	), nil
}

func (s strictOpenAPIServer) CancelQueuedBacklogInput(
	ctx context.Context,
	request openapi.CancelQueuedBacklogInputRequestObject,
) (openapi.CancelQueuedBacklogInputResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.cancelQueuedBacklogInput(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) cancelQueuedBacklogInput(
	ctx context.Context,
	request openapi.CancelQueuedBacklogInputRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.CancelQueuedBacklogInputResponseObject, error) {
	inputID, ok := parseOpenAPIPublicID(publicid.KindAgentInput, request.InputID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	err := s.server.store.Execution().CancelQueuedBacklogInput(ctx, executionstore.CancelQueuedBacklogInputInput{
		ProjectID: project.ID,
		AgentID:   agent.ID,
		InputID:   inputID,
	})
	if err != nil {
		return nil, agentInputCommandAPIError(err)
	}
	return openapi.CancelQueuedBacklogInput200JSONResponse(openapi.OKResponse{Ok: true}), nil
}

func (s strictOpenAPIServer) MoveQueuedBacklogInput(
	ctx context.Context,
	request openapi.MoveQueuedBacklogInputRequestObject,
) (openapi.MoveQueuedBacklogInputResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.moveQueuedBacklogInput(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) moveQueuedBacklogInput(
	ctx context.Context,
	request openapi.MoveQueuedBacklogInputRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.MoveQueuedBacklogInputResponseObject, error) {
	inputID, ok := parseOpenAPIPublicID(publicid.KindAgentInput, request.InputID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	anchorID := storage.NilID
	if request.Body.AnchorInputId != nil && *request.Body.AnchorInputId != "" {
		var ok bool
		anchorID, ok = parseOpenAPIPublicID(publicid.KindAgentInput, *request.Body.AnchorInputId)
		if !ok {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid anchor_input_id")
		}
	}
	err := s.server.store.Execution().MoveQueuedBacklogInput(ctx, executionstore.MoveQueuedBacklogInputInput{
		ProjectID:     project.ID,
		AgentID:       agent.ID,
		InputID:       inputID,
		Position:      executionstore.MoveQueuedBacklogInputPosition(request.Body.Position),
		AnchorInputID: anchorID,
	})
	if err != nil {
		return nil, agentInputCommandAPIError(err)
	}
	return openapi.MoveQueuedBacklogInput200JSONResponse(openapi.OKResponse{Ok: true}), nil
}

func (s strictOpenAPIServer) PromoteQueuedInputToSteering(
	ctx context.Context,
	request openapi.PromoteQueuedInputToSteeringRequestObject,
) (openapi.PromoteQueuedInputToSteeringResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.promoteQueuedInputToSteering(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) promoteQueuedInputToSteering(
	ctx context.Context,
	request openapi.PromoteQueuedInputToSteeringRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.PromoteQueuedInputToSteeringResponseObject, error) {
	inputID, ok := parseOpenAPIPublicID(publicid.KindAgentInput, request.InputID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	cancelOpenInteractions := request.Body != nil &&
		request.Body.CancelOpenInteractions != nil &&
		*request.Body.CancelOpenInteractions
	err := s.server.store.Execution().PromoteQueuedInputToSteering(
		ctx,
		executionstore.PromoteQueuedInputToSteeringInput{
			ProjectID:              project.ID,
			AgentID:                agent.ID,
			InputID:                inputID,
			CancelOpenInteractions: cancelOpenInteractions,
		},
	)
	if err != nil {
		return nil, agentInputCommandAPIError(err)
	}
	return openapi.PromoteQueuedInputToSteering200JSONResponse(openapi.OKResponse{Ok: true}), nil
}

func (s strictOpenAPIServer) DemoteSteeringInputToQueued(
	ctx context.Context,
	request openapi.DemoteSteeringInputToQueuedRequestObject,
) (openapi.DemoteSteeringInputToQueuedResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.demoteSteeringInputToQueued(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) demoteSteeringInputToQueued(
	ctx context.Context,
	request openapi.DemoteSteeringInputToQueuedRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.DemoteSteeringInputToQueuedResponseObject, error) {
	inputID, ok := parseOpenAPIPublicID(publicid.KindAgentInput, request.InputID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	err := s.server.store.Execution().DemoteSteeringInputToQueued(
		ctx,
		executionstore.DemoteSteeringInputToQueuedInput{
			ProjectID: project.ID,
			AgentID:   agent.ID,
			InputID:   inputID,
		},
	)
	if err != nil {
		return nil, agentInputCommandAPIError(err)
	}
	return openapi.DemoteSteeringInputToQueued200JSONResponse(openapi.OKResponse{Ok: true}), nil
}

func (s strictOpenAPIServer) CreateAgentInput(
	ctx context.Context,
	request openapi.CreateAgentInputRequestObject,
) (openapi.CreateAgentInputResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.createAgentInput(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) createAgentInput(
	ctx context.Context,
	request openapi.CreateAgentInputRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.CreateAgentInputResponseObject, error) {
	if request.Body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, ok := principalFromContext(ctx)
	if !ok || principal.ID == storage.NilID {
		return nil, apierror.FromCode(openapi.ErrorCodeUnauthorized, "unauthorized")
	}
	actor, err := requestActorParams(project, principal, request.Body.Actor)
	if err != nil {
		return nil, err
	}
	idempotencyKey := ""
	if request.Params.IdempotencyKey != nil {
		idempotencyKey = *request.Params.IdempotencyKey
	}
	var deliveryMode executionstore.AgentInputDeliveryMode
	if request.Body.DeliveryMode != nil {
		deliveryMode = executionstore.AgentInputDeliveryMode(*request.Body.DeliveryMode)
	}
	cancelOpenInteractions := request.Body.CancelOpenInteractions != nil &&
		*request.Body.CancelOpenInteractions
	if cancelOpenInteractions && deliveryMode != executionstore.DeliveryModeSteering {
		return nil, apierror.FromCode(
			openapi.ErrorCodeInvalidRequest,
			"cancel_open_interactions is allowed only for steering inputs",
		)
	}
	contentBlocks, err := rawJSONFromContentBlocks(request.Body.ContentBlocks)
	if err != nil {
		return nil, err
	}
	inputBlocks, err := s.server.extractInlineMedia(ctx, mediaIngestContext{
		ProjectID:      project.ID,
		AgentID:        agent.ID,
		IdempotencyKey: idempotencyKey,
	}, contentBlocks)
	if err != nil {
		return nil, mediaIngestAPIError(err)
	}
	agentInput, storedContentBlocks, created, err := s.server.store.Execution().CreateAgentContentInput(
		ctx,
		executionstore.CreateAgentContentInputInput{
			ProjectID:              project.ID,
			AgentID:                agent.ID,
			Actor:                  actor,
			ContentBlocks:          inputBlocks,
			DeliveryMode:           deliveryMode,
			IdempotencyKey:         idempotencyKey,
			CancelOpenInteractions: cancelOpenInteractions,
		},
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	inputResponse, err := publicAgentInputResponseFromRecordWithContent(agentInput, storedContentBlocks)
	if err != nil {
		return nil, err
	}
	response := openapi.AgentInputEnvelope{AgentInput: inputResponse}
	if created {
		return openapi.CreateAgentInput201JSONResponse(response), nil
	}
	return openapi.CreateAgentInput200JSONResponse(response), nil
}

func mediaIngestAPIError(err error) apierror.ResponseError {
	var ingestErr mediaIngestError
	if errors.As(err, &ingestErr) {
		return apierror.FromCode(openapi.ErrorCodeInvalidRequest, ingestErr.Error())
	}
	return apierror.ProjectScoped(err)
}

func (s strictOpenAPIServer) ListEvents(
	ctx context.Context,
	request openapi.ListEventsRequestObject,
) (openapi.ListEventsResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listEvents(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) listEvents(
	ctx context.Context,
	request openapi.ListEventsRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.ListEventsResponseObject, error) {
	limit, err := timelineLimit(request.Params.Limit, defaultEventPageLimit, maxEventPageLimit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	if request.Params.BeforeSequence != nil {
		return s.listEventsBefore(ctx, request, project, agent, limit)
	}
	afterSequence, err := sequenceBoundary(request.Params.AfterSequence, "after_sequence")
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	events, err := s.server.store.Execution().ListAgentEventsForRead(ctx, project.ID, agent.ID, afterSequence, limit+1)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	hasMore := len(events) > int(limit)
	if hasMore {
		events = events[:limit]
	}
	nextAfterSequence := afterSequence
	if len(events) > 0 {
		nextAfterSequence = events[len(events)-1].Sequence
	}
	response, err := publicEventResponsesFromReadRecords(events)
	if err != nil {
		return nil, err
	}
	return openapi.ListEvents200JSONResponse(openapi.ListAgentEventsResponse{
		Data:              response,
		HasMore:           hasMore,
		NextAfterSequence: nextAfterSequence,
	}), nil
}

// listEventsBefore serves one older event page, chronological within the
// page: before_sequence 0 means the latest events, and next_before_sequence
// walks toward the beginning of the log.
func (s strictOpenAPIServer) listEventsBefore(
	ctx context.Context,
	request openapi.ListEventsRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
	limit int32,
) (openapi.ListEventsResponseObject, error) {
	beforeSequence, err := sequenceBoundary(request.Params.BeforeSequence, "before_sequence")
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	events, err := s.server.store.Execution().ListAgentEventsBeforeForRead(
		ctx,
		project.ID,
		agent.ID,
		beforeSequence,
		limit+1,
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	events, nextBeforeSequence := trimEventsBeforePage(events, limit)
	nextAfterSequence := beforeSequence
	if len(events) > 0 {
		nextAfterSequence = events[len(events)-1].Sequence
	}
	response, err := publicEventResponsesFromReadRecords(events)
	if err != nil {
		return nil, err
	}
	return openapi.ListEvents200JSONResponse(openapi.ListAgentEventsResponse{
		Data:               response,
		HasMore:            nextBeforeSequence != nil,
		NextAfterSequence:  nextAfterSequence,
		NextBeforeSequence: nullableFromPtr(nextBeforeSequence),
	}), nil
}

func (s strictOpenAPIServer) ListTurns(
	ctx context.Context,
	request openapi.ListTurnsRequestObject,
) (openapi.ListTurnsResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listTurns(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) listTurns(
	ctx context.Context,
	request openapi.ListTurnsRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.ListTurnsResponseObject, error) {
	beforeTurnSequence, err := sequenceBoundary(request.Params.BeforeTurnSequence, "before_turn_sequence")
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	limit, err := timelineLimit(request.Params.Limit, defaultTurnPageLimit, maxTurnPageLimit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	turns, err := s.server.store.Execution().ListAgentTurnsForRead(ctx, project.ID, agent.ID, beforeTurnSequence, limit+1)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	hasMore := len(turns) > int(limit)
	if hasMore {
		turns = turns[:limit]
	}
	var nextBeforeTurnSequence *int64
	if hasMore && len(turns) > 0 {
		nextBeforeTurnSequence = int64Ptr(turns[len(turns)-1].TurnSequence)
	}
	response, err := publicTurnResponsesFromReadRecords(turns)
	if err != nil {
		return nil, err
	}
	return openapi.ListTurns200JSONResponse(openapi.ListAgentTurnsResponse{
		Data:                   response,
		NextBeforeTurnSequence: nullableFromPtr(nextBeforeTurnSequence),
	}), nil
}

func (s strictOpenAPIServer) ListTurnEvents(
	ctx context.Context,
	request openapi.ListTurnEventsRequestObject,
) (openapi.ListTurnEventsResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.listTurnEvents(ctx, request, scope.project, scope.agent)
}

func (s strictOpenAPIServer) listTurnEvents(
	ctx context.Context,
	request openapi.ListTurnEventsRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.ListTurnEventsResponseObject, error) {
	turnID, ok := parseOpenAPIPublicID(publicid.KindAgentTurn, request.TurnID)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
	}
	beforeSequence, err := sequenceBoundary(request.Params.BeforeSequence, "before_sequence")
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	limit, err := timelineLimit(request.Params.Limit, defaultEventPageLimit, maxEventPageLimit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	events, err := s.server.store.Execution().ListTurnEventsForRead(
		ctx,
		project.ID,
		agent.ID,
		turnID,
		beforeSequence,
		limit+1,
	)
	if err != nil {
		return nil, apierror.ProjectScoped(err)
	}
	events, nextBeforeSequence := trimEventsBeforePage(events, limit)
	response, err := publicEventResponsesFromReadRecords(events)
	if err != nil {
		return nil, err
	}
	return openapi.ListTurnEvents200JSONResponse(openapi.ListTurnEventsResponse{
		Data:               response,
		NextBeforeSequence: nullableFromPtr(nextBeforeSequence),
	}), nil
}

func (s strictOpenAPIServer) StreamEvents(
	ctx context.Context,
	request openapi.StreamEventsRequestObject,
) (openapi.StreamEventsResponseObject, error) {
	scope, err := agentScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	return s.streamEvents(ctx, request, scope.project, scope.agent)
}

const (
	streamFrameChannelSize            = 4096
	// The TypeScript SDK treats 35s of silence as a stalled connection (stallTimeoutMs); stay well below that.
	agentEventStreamHeartbeatInterval = 10 * time.Second
)

func (s strictOpenAPIServer) streamEvents(
	ctx context.Context,
	request openapi.StreamEventsRequestObject,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
) (openapi.StreamEventsResponseObject, error) {
	r, ok := openAPIHTTPRequest(ctx)
	if !ok {
		return nil, apierror.FromCode(openapi.ErrorCodeServiceUnavailable, "event stream request is unavailable")
	}
	after := int64(0)
	if request.Params.AfterSequence != nil {
		if *request.Params.AfterSequence < 0 {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "after_sequence must be a non-negative integer")
		}
		after = *request.Params.AfterSequence
	}
	if request.Params.LastEventID != nil {
		if *request.Params.LastEventID < 0 {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "Last-Event-ID must be a non-negative event sequence")
		}
		after = *request.Params.LastEventID
	}
	streamDeltas := request.Params.StreamDeltas != nil && *request.Params.StreamDeltas
	return streamEventsLiveResponse{
		server:       s.server,
		request:      r,
		project:      project,
		agent:        agent,
		after:        after,
		streamDeltas: streamDeltas,
	}, nil
}

type streamEventsLiveResponse struct {
	server       *Server
	request      *http.Request
	project      identitystore.ProjectRecord
	agent        executionstore.AgentRecord
	after        int64
	streamDeltas bool
}

func (response streamEventsLiveResponse) VisitStreamEventsResponse(w http.ResponseWriter) error {
	if _, ok := w.(http.Flusher); !ok {
		return openapi.StreamEvents503JSONResponse{
			ServiceUnavailableJSONResponse: openapi.ServiceUnavailableJSONResponse(
				apierror.Body(openapi.ErrorCodeServiceUnavailable, "streaming unsupported"),
			),
		}.VisitStreamEventsResponse(
			w,
		)
	}
	response.server.streamAgentEvents(
		w,
		response.request,
		response.project,
		response.agent,
		response.after,
		response.streamDeltas,
	)
	return nil
}

func (s *Server) streamAgentEvents(
	w http.ResponseWriter,
	r *http.Request,
	project identitystore.ProjectRecord,
	agent executionstore.AgentRecord,
	after int64,
	streamDeltasEnabled bool,
) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "streaming unsupported")
		return
	}
	notify := make(chan struct{}, 1)
	toolCallUpdates := make(chan notifications.ToolCallUpdatedCommitted, streamFrameChannelSize)
	var streamDeltas chan json.RawMessage
	if streamDeltasEnabled {
		streamDeltas = make(chan json.RawMessage, streamFrameChannelSize)
	}
	eventSubscription, subErr := s.agentEventWakeupSubscriber.SubscribeAgentEventWakeups(
		r.Context(),
		agent.ID,
		func(context.Context) {
			select {
			case notify <- struct{}{}:
			default:
			}
		},
	)
	if subErr != nil {
		s.log.Warn("agent event subscribe failed", "agent_id", agent.ID, "error", subErr)
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "event stream temporarily unavailable")
		return
	}
	defer func() { _ = eventSubscription.Unsubscribe() }()
	toolCallSubscription, subErr := s.agentToolCallUpdateSubscriber.SubscribeAgentToolCallUpdates(
		r.Context(),
		agent.ID,
		func(_ context.Context, update notifications.ToolCallUpdatedCommitted) {
			select {
			case toolCallUpdates <- update:
			default:
				if s.log != nil {
					s.log.Debug(
						"drop tool call update because subscriber buffer is full",
						"agent_id",
						agent.ID,
					)
				}
			}
		},
	)
	if subErr != nil {
		s.log.Warn("tool call update subscribe failed", "agent_id", agent.ID, "error", subErr)
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "event stream temporarily unavailable")
		return
	}
	defer func() { _ = toolCallSubscription.Unsubscribe() }()
	var streamSubscription notifications.Subscription
	if streamDeltas != nil {
		streamSubscription, subErr = s.agentStreamDeltaSubscriber.SubscribeAgentStreamDeltas(
			r.Context(),
			agent.ID,
			func(_ context.Context, payload json.RawMessage) {
				if len(payload) == 0 {
					return
				}
				select {
				case streamDeltas <- payload:
				default:
					if s.log != nil {
						s.log.Debug(
							"drop agent stream delta because subscriber buffer is full",
							"agent_id",
							agent.ID,
						)
					}
				}
			},
		)
		if subErr != nil {
			s.log.Warn("agent stream subscribe failed", "agent_id", agent.ID, "error", subErr)
			apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "event stream temporarily unavailable")
			return
		}
		defer func() { _ = streamSubscription.Unsubscribe() }()
	}
	if s.agentEventStreamReconciler == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "event stream temporarily unavailable")
		return
	}
	reconciliation, ok := s.agentEventStreamReconciler.register(agent.ID, after, notify)
	if !ok {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable, "event stream temporarily unavailable")
		return
	}
	defer reconciliation.unregister()
	streamCtx, cancelStream := context.WithCancel(r.Context())
	//nolint:contextcheck // server shutdown must also cancel this request-derived stream context
	stopCloseWatch := context.AfterFunc(reconciliation.closeContext(), cancelStream)
	defer func() {
		stopCloseWatch()
		cancelStream()
	}()
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	heartbeat := s.timer.Ticker(agentEventStreamHeartbeatInterval)
	defer heartbeat.Stop()
	if _, err := fmt.Fprint(w, ": ok\n\n"); err != nil {
		return
	}
	flusher.Flush()
	completedModelCallContexts := map[string]struct{}{}
	for {
		select {
		case <-streamCtx.Done():
			return
		default:
		}
		records, err := s.store.Execution().ListAgentEventsForRead(streamCtx, project.ID, agent.ID, after, 100)
		if err != nil {
			if streamCtx.Err() != nil {
				return
			}
			if !writeSSEJSONFrame(
				w,
				"error",
				"",
				apierror.Body(openapi.ErrorCodeServiceUnavailable, "event stream unavailable"),
			) {
				return
			}
			flusher.Flush()
			return
		}
		for _, record := range records {
			response, err := publicEventResponseFromReadRecord(record)
			if err != nil {
				if !writeSSEJSONFrame(w, "error", "", apierror.Body(openapi.ErrorCodeInternalError)) {
					return
				}
				flusher.Flush()
				return
			}
			if !writeSSEJSONFrame(w, record.EventKind, strconv.FormatInt(record.Sequence, 10), response) {
				return
			}
			if record.EventKind == "model_output" &&
				record.ModelCallContextID != storage.NilID {
				contextID, err := publicID(
					publicid.KindModelCallContext,
					record.ModelCallContextID,
				)
				if err != nil {
					if !writeSSEJSONFrame(
						w,
						"error",
						"",
						apierror.Body(openapi.ErrorCodeInternalError),
					) {
						return
					}
					flusher.Flush()
					return
				}
				completedModelCallContexts[contextID] = struct{}{}
			}
			after = record.Sequence
			reconciliation.advance(after)
			flusher.Flush()
		}
		if len(records) == 100 {
			continue
		}
	waitForDurableWakeup:
		for {
			// Prefer a buffered durable wakeup; at most one racing best-effort
			// frame can precede reconciliation.
			select {
			case <-notify:
				break waitForDurableWakeup
			default:
			}
			select {
			case <-streamCtx.Done():
				return
			case <-notify:
				break waitForDurableWakeup
			case update := <-toolCallUpdates:
				if !writeToolCallUpdateFrame(w, update) {
					flusher.Flush()
					return
				}
				flusher.Flush()
			case payload := <-streamDeltas:
				if !writeModelOutputDeltaFrame(w, payload, completedModelCallContexts) {
					return
				}
				flusher.Flush()
			case <-heartbeat.C:
				if _, err := fmt.Fprint(w, ": heartbeat\n\n"); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	}
}

func writeToolCallUpdateFrame(w http.ResponseWriter, update notifications.ToolCallUpdatedCommitted) bool {
	toolCallID, err := publicID(publicid.KindToolCall, update.ToolCallID)
	state := openapi.ToolCallState(update.State)
	if err != nil {
		_ = writeSSEJSONFrame(w, "error", "", apierror.Body(openapi.ErrorCodeInternalError))
		return false
	}
	if !state.Valid() {
		return true
	}
	return writeSSEJSONFrame(w, "tool_call_update", "", openapi.ToolCallUpdate{
		ToolCallId: toolCallID,
		State:      state,
	})
}

func writeModelOutputDeltaFrame(
	w http.ResponseWriter,
	payload json.RawMessage,
	completedModelCallContexts map[string]struct{},
) bool {
	var envelope struct {
		ModelCallContextID string `json:"model_call_context_id"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return true
	}
	if envelope.ModelCallContextID != "" {
		if _, completed := completedModelCallContexts[envelope.ModelCallContextID]; completed {
			return true
		}
	}
	return writeSSEFrame(w, "model_output_delta", "", string(payload))
}

func sequenceBoundary(value *int64, name string) (int64, error) {
	if value == nil {
		return 0, nil
	}
	if *value < 0 {
		return 0, errors.New(name + " must be a non-negative integer")
	}
	return *value, nil
}

func timelineLimit(value *int32, defaultValue, maxValue int32) (int32, error) {
	if value == nil {
		return defaultValue, nil
	}
	if *value < 1 || *value > maxValue {
		return 0, fmt.Errorf("limit must be an integer between 1 and %d", maxValue)
	}
	return *value, nil
}

func int64Ptr(value int64) *int64 {
	return &value
}

func trimEventsBeforePage(
	events []executionstore.AgentEventReadRecord,
	limit int32,
) ([]executionstore.AgentEventReadRecord, *int64) {
	if len(events) <= int(limit) {
		return events, nil
	}
	nextBeforeSequence := events[1].Sequence
	return events[1:], &nextBeforeSequence
}

func writeSSEJSONFrame(w http.ResponseWriter, eventName, id string, value any) bool {
	body, err := json.Marshal(value)
	if err != nil {
		return false
	}
	return writeSSEFrame(w, eventName, id, string(body))
}

func writeSSEFrame(w http.ResponseWriter, eventName, id, data string) bool {
	if strings.ContainsAny(eventName, "\r\n") || strings.ContainsAny(id, "\x00\r\n") {
		return false
	}
	if id != "" {
		if _, err := fmt.Fprintf(w, "id: %s\n", id); err != nil {
			return false
		}
	}
	if eventName != "" {
		if _, err := fmt.Fprintf(w, "event: %s\n", eventName); err != nil {
			return false
		}
	}
	if !strings.ContainsAny(data, "\r\n") {
		_, err := fmt.Fprintf(w, "data: %s\n\n", data)
		return err == nil
	}
	data = strings.ReplaceAll(data, "\r\n", "\n")
	data = strings.ReplaceAll(data, "\r", "\n")
	for _, line := range strings.Split(data, "\n") {
		if _, err := fmt.Fprintf(w, "data: %s\n", line); err != nil {
			return false
		}
	}
	if _, err := fmt.Fprint(w, "\n"); err != nil {
		return false
	}
	return true
}
