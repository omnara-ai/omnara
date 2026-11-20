package httpapi

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/agentconfigcompile"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type agentConfigEditRequest struct {
	project        identitystore.ProjectRecord
	principal      identitystore.PrincipalRecord
	edits          []agentconfig.Edit
	allowPartial   bool
	rolloutKey     string
	deleteMachines []executionstore.MachineRecord
}

type agentConfigEditSelection struct {
	listed        []agentConfigEditTarget
	listedAgents  map[uuid.UUID]bool
	profileIDs    []uuid.UUID
	includeAgents bool
}

type agentConfigEditCursor struct {
	Query     string    `json:"query"`
	Rollout   string    `json:"rollout"`
	Index     int       `json:"index"`
	Before    time.Time `json:"before"`
	CreatedAt time.Time `json:"created_at"`
	AgentID   string    `json:"id,omitempty"`
	agentID   uuid.UUID
}

type agentConfigEditTarget struct {
	profile bool
	id      uuid.UUID
	next    agentConfigEditCursor
}

func (s strictOpenAPIServer) EditAgentConfigs(
	ctx context.Context,
	request openapi.EditAgentConfigsRequestObject,
) (openapi.EditAgentConfigsResponseObject, error) {
	scope, err := projectScopeFromContext(ctx)
	if err != nil {
		return nil, err
	}
	body := request.Body
	if body == nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "request body is required")
	}
	principal, _ := principalFromContext(ctx)
	profileIDs, err := publicIDsFromParams(publicid.KindAgentProfile, "agent_profile_ids", body.AgentProfileIds)
	if err != nil {
		return nil, err
	}
	agentIDs, err := publicIDsFromParams(publicid.KindAgent, "agent_ids", body.AgentIds)
	if err != nil {
		return nil, err
	}
	includeProfiles := true
	sel := agentConfigEditSelection{profileIDs: profileIDs, includeAgents: true}
	if body.ApplyTo != nil {
		if len(profileIDs) == 0 {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "apply_to requires agent_profile_ids")
		}
		includeProfiles = slices.Contains(*body.ApplyTo, openapi.EditAgentConfigsRequestApplyToProfile)
		sel.includeAgents = slices.Contains(*body.ApplyTo, openapi.EditAgentConfigsRequestApplyToAgent)
	}
	compareIDs := func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) }
	slices.SortFunc(profileIDs, compareIDs)
	slices.SortFunc(agentIDs, compareIDs)
	if includeProfiles {
		for _, id := range profileIDs {
			sel.listed = append(sel.listed, agentConfigEditTarget{profile: true, id: id})
		}
	}
	sel.listedAgents = make(map[uuid.UUID]bool, len(agentIDs))
	for _, id := range agentIDs {
		sel.listed = append(sel.listed, agentConfigEditTarget{id: id})
		sel.listedAgents[id] = true
	}
	edits, err := agentConfigEdits(body.Edits)
	if err != nil {
		return nil, err
	}
	limit, err := parseOpenAPIPageLimit(request.Params.Limit)
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
	}
	query, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	position := agentConfigEditCursor{Query: shortHash(scope.project.ID.String() + string(query))}
	if request.Params.Cursor != nil && *request.Params.Cursor != "" {
		if position, err = decodeAgentConfigEditCursor(*request.Params.Cursor, position.Query); err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error())
		}
	} else {
		if request.Params.IdempotencyKey != nil && *request.Params.IdempotencyKey != "" {
			position.Rollout = shortHash(*request.Params.IdempotencyKey)
		}
		var profileKeys []string
		if includeProfiles && position.Rollout != "" {
			for _, profileID := range profileIDs {
				id, err := publicID(publicid.KindAgentProfile, profileID)
				if err != nil {
					return nil, err
				}
				profileKeys = append(profileKeys, agentConfigEditKey(position.Rollout, id))
			}
		}
		check, err := s.server.store.Execution().CheckAgentConfigEditTargets(
			ctx, scope.project.ID, profileIDs, agentIDs, profileKeys,
		)
		if err != nil {
			return nil, err
		}
		if check.Profiles != int64(len(profileIDs)) || check.Agents != int64(len(agentIDs)) {
			return nil, apierror.FromCode(openapi.ErrorCodeNotFound, "not found")
		}
		position.Before = check.Cutoff
	}
	targets, err := s.agentConfigEditTargets(ctx, scope.project.ID, sel, position, limit+1)
	if err != nil {
		return nil, err
	}
	hasMore := len(targets) > limit
	if hasMore {
		targets = targets[:limit]
	}
	edit := &agentConfigEditRequest{
		project: scope.project, principal: principal, edits: edits, allowPartial: body.AllowPartialEdits,
		rolloutKey: position.Rollout,
	}
	defer func() { s.server.startPoolMachineDeletion(ctx, edit.deleteMachines) }()
	response := openapi.EditAgentConfigs200JSONResponse{
		Data:       make([]openapi.AgentConfigEditResult, 0, len(targets)),
		NextCursor: nullableFromPtr[string](nil),
	}
	for _, target := range targets {
		var result openapi.AgentConfigEditResult
		if target.profile {
			result, err = s.editAgentProfileConfig(ctx, edit, target.id)
		} else {
			result, err = s.editAgentConfig(ctx, edit, target.id)
		}
		if err != nil {
			return nil, err
		}
		if result.Status != openapi.AgentConfigEditResultStatusUpdated && result.Edits != nil {
			for i, outcome := range *result.Edits {
				if outcome == openapi.AgentConfigEditResultEditsApplied {
					(*result.Edits)[i] = openapi.AgentConfigEditResultEditsNotApplied
				}
			}
		}
		response.Data = append(response.Data, result)
	}
	if hasMore {
		cursor, err := encodeAgentConfigEditCursor(targets[len(targets)-1].next, position)
		if err != nil {
			return nil, err
		}
		response.NextCursor = nullableFromValue(cursor)
	}
	return response, nil
}

func (s strictOpenAPIServer) agentConfigEditTargets(
	ctx context.Context,
	projectID uuid.UUID,
	sel agentConfigEditSelection,
	pos agentConfigEditCursor,
	want int,
) ([]agentConfigEditTarget, error) {
	var targets []agentConfigEditTarget
	for ; pos.Index < len(sel.listed); pos.Index++ {
		target := sel.listed[pos.Index]
		target.next = agentConfigEditCursor{Index: pos.Index + 1, Before: pos.Before}
		if targets = append(targets, target); len(targets) == want {
			return targets, nil
		}
	}
	store := s.server.store.Execution()
	for ; sel.includeAgents && pos.Index-len(sel.listed) < len(sel.profileIDs); pos.Index++ {
		profileID := sel.profileIDs[pos.Index-len(sel.listed)]
		for {
			batchSize := want - len(targets)
			batch, err := store.ListAgentConfigEditProfileAgents(ctx, projectID, profileID,
				executionstore.AgentConfigEditProfileAgentsWindow{
					CreatedBefore: pos.Before,
					After:         executionstore.AgentConfigEditProfileAgent{CreatedAt: pos.CreatedAt, ID: pos.agentID},
				}, batchSize)
			if err != nil {
				return nil, err
			}
			for _, agent := range batch {
				pos.CreatedAt, pos.agentID = agent.CreatedAt, agent.ID
				if sel.listedAgents[agent.ID] {
					continue
				}
				if targets = append(targets, agentConfigEditTarget{id: agent.ID, next: pos}); len(targets) == want {
					return targets, nil
				}
			}
			if len(batch) < batchSize {
				break
			}
		}
		pos.CreatedAt, pos.agentID = time.Time{}, uuid.Nil
	}
	return targets, nil
}

func (s strictOpenAPIServer) editAgentConfig(
	ctx context.Context,
	edit *agentConfigEditRequest,
	agentID uuid.UUID,
) (openapi.AgentConfigEditResult, error) {
	store := s.server.store.Execution()
	id, err := publicID(publicid.KindAgent, agentID)
	if err != nil {
		return openapi.AgentConfigEditResult{}, err
	}
	result := openapi.AgentConfigEditResult{Type: openapi.AgentConfigEditResultTypeAgent, AgentId: &id}
	agent, err := store.GetAgentInProject(ctx, edit.project.ID, agentID)
	if err != nil {
		return agentConfigEditFailed(result, err)
	}
	key := agentConfigEditKey(edit.rolloutKey, id)
	if key != "" {
		configID, found, err := store.AgentConfigChangeByIdempotencyKey(ctx, edit.project.ID, agentID, key)
		if err != nil {
			return agentConfigEditFailed(result, err)
		}
		if found {
			return agentConfigEditUpdated(result, configID, configID)
		}
	}
	if agent.State != executionstore.AgentStateActive {
		return agentConfigEditFailed(result,
			apierror.FromCode(openapi.ErrorCodeStateTransitionConflict, "agent is archived"))
	}
	config, found, err := store.GetAgentConfig(ctx, edit.project.ID, agent.CurrentConfigID)
	if err == nil && !found {
		err = storeerr.ErrNotFound
	}
	if err != nil {
		return agentConfigEditFailed(result, err)
	}
	compiled, ok, err := s.editedAgentConfig(ctx, edit, config, &result)
	if err != nil || !ok {
		return result, err
	}
	change, err := store.ChangeAgentConfig(ctx, executionstore.ChangeAgentConfigInput{
		CreateAgentConfigInput:  compiled.CreateInput(edit.project.ID),
		AgentID:                 agentID,
		ExpectedCurrentConfigID: config.ID,
		ActorType:               edit.principal.Type,
		ActorID:                 edit.principal.ID,
		Reason:                  "api",
		IdempotencyKey:          key,
	})
	if err != nil {
		return agentConfigEditFailed(result, err)
	}
	edit.deleteMachines = append(edit.deleteMachines, change.DeleteMachines...)
	if !change.Created {
		result.Edits = nil
		return agentConfigEditUpdated(result, change.AgentConfig.ID, change.AgentConfig.ID)
	}
	return agentConfigEditUpdated(result, config.ID, change.AgentConfig.ID)
}

func (s strictOpenAPIServer) editAgentProfileConfig(
	ctx context.Context,
	edit *agentConfigEditRequest,
	profileID uuid.UUID,
) (openapi.AgentConfigEditResult, error) {
	store := s.server.store.Execution()
	id, err := publicID(publicid.KindAgentProfile, profileID)
	if err != nil {
		return openapi.AgentConfigEditResult{}, err
	}
	result := openapi.AgentConfigEditResult{Type: openapi.AgentConfigEditResultTypeProfile, AgentProfileId: &id}
	key := agentConfigEditKey(edit.rolloutKey, id)
	replay := func() (openapi.AgentConfigEditResult, bool, error) {
		configID, generation, found, err := store.AgentProfileVersionByIdempotencyKey(ctx, edit.project.ID, profileID, key)
		if err != nil || !found {
			return openapi.AgentConfigEditResult{}, false, err
		}
		replayed := result
		replayed.Edits, replayed.CurrentGeneration = nil, &generation
		replayed, err = agentConfigEditUpdated(replayed, configID, configID)
		return replayed, true, err
	}
	profile, err := store.GetAgentProfile(ctx, edit.project.ID, profileID)
	if err != nil {
		return agentConfigEditFailed(result, err)
	}
	if key != "" {
		if replayed, found, err := replay(); err != nil || found {
			return replayed, err
		}
	}
	result.CurrentGeneration = &profile.CurrentGeneration
	compiled, ok, err := s.editedAgentConfig(ctx, edit, profile.CurrentConfig, &result)
	if err != nil || !ok {
		return result, err
	}
	config, err := store.CreateAgentConfig(ctx, compiled.CreateInput(edit.project.ID))
	if err != nil {
		return agentConfigEditFailed(result, err)
	}
	updated, err := store.RetargetAgentProfile(ctx, executionstore.RetargetAgentProfileInput{
		ProjectID:               edit.project.ID,
		ProfileID:               profileID,
		ExpectedCurrentConfigID: profile.CurrentConfigID,
		ConfigID:                config.ID,
		IdempotencyKey:          key,
	})
	if errors.Is(err, storeerr.ErrIdempotencyConflict) || (err == nil && !updated.Created) {
		if replayed, found, replayErr := replay(); replayErr != nil || found {
			return replayed, replayErr
		}
	}
	if err != nil {
		return agentConfigEditFailed(result, err)
	}
	result.CurrentGeneration = &updated.CurrentGeneration
	return agentConfigEditUpdated(result, profile.CurrentConfigID, updated.CurrentConfigID)
}

func (s strictOpenAPIServer) editedAgentConfig(
	ctx context.Context,
	edit *agentConfigEditRequest,
	config executionstore.AgentConfigRecord,
	result *openapi.AgentConfigEditResult,
) (agentconfigcompile.Body, bool, error) {
	format, source := agentconfig.SourceFormat(config.SourceFormat), config.Source
	if source == "" {
		format = agentconfig.SourceFormatJSON
		generated, err := agentconfigcompile.GenerateSource(
			ctx, s.server.store, edit.project.OrgID, edit.project.ID, config, format,
		)
		if err != nil {
			*result, err = agentConfigEditFailed(*result, err)
			return agentconfigcompile.Body{}, false, err
		}
		source = generated
	}
	edited, err := agentconfig.ApplyEdits(format, source, edit.edits)
	if err != nil {
		*result, err = agentConfigEditFailed(*result, apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error()))
		return agentconfigcompile.Body{}, false, err
	}
	outcomes := make([]openapi.AgentConfigEditResultEdits, len(edited.Edits))
	var missed []int
	for i, outcome := range edited.Edits {
		outcomes[i] = openapi.AgentConfigEditResultEdits(outcome)
		if outcome == "text_not_found" || outcome == "path_not_found" {
			missed = append(missed, i)
		}
	}
	result.Edits = &outcomes
	if !edited.Changed {
		result.Status = openapi.AgentConfigEditResultStatusUnchanged
		result.CurrentConfigId, err = idOrNil(publicid.KindAgentConfig, config.ID)
		return agentconfigcompile.Body{}, false, err
	}
	if len(missed) > 0 && !edit.allowPartial {
		*result, err = agentConfigEditFailed(*result,
			apierror.FromCode(openapi.ErrorCodeEditConflict, agentConfigEditConflictMessage(missed)))
		return agentconfigcompile.Body{}, false, err
	}
	compiled, err := agentconfigcompile.Compile(
		ctx, s.server.store, edit.project.OrgID, edit.project.ID, s.server.agentConfigOptions, format, edited.Source,
	)
	if err != nil {
		*result, err = agentConfigEditFailed(*result, agentConfigCompileError(err))
		return agentconfigcompile.Body{}, false, err
	}
	return compiled, true, nil
}

func agentConfigEditUpdated(
	result openapi.AgentConfigEditResult,
	previousID, currentID uuid.UUID,
) (openapi.AgentConfigEditResult, error) {
	var err error
	result.Status = openapi.AgentConfigEditResultStatusUpdated
	if previousID != currentID {
		if result.PreviousConfigId, err = idOrNil(publicid.KindAgentConfig, previousID); err != nil {
			return openapi.AgentConfigEditResult{}, err
		}
	}
	if result.CurrentConfigId, err = idOrNil(publicid.KindAgentConfig, currentID); err != nil {
		return openapi.AgentConfigEditResult{}, err
	}
	return result, nil
}

func agentConfigEditFailed(result openapi.AgentConfigEditResult, err error) (openapi.AgentConfigEditResult, error) {
	var responseErr apierror.ResponseError
	if !errors.As(err, &responseErr) {
		responseErr = apierror.ProjectScoped(err)
	}
	if responseErr.Status >= http.StatusInternalServerError {
		return openapi.AgentConfigEditResult{}, err
	}
	result.Status = openapi.AgentConfigEditResultStatusFailed
	result.Code, result.Error = &responseErr.Code, &responseErr.Message
	return result, nil
}

func agentConfigEditConflictMessage(missed []int) string {
	edits := make([]string, len(missed))
	for i, index := range missed {
		edits[i] = strconv.Itoa(index)
	}
	subject := "edit " + edits[0]
	if last := len(edits) - 1; last > 0 {
		subject = "edits " + strings.Join(edits[:last], ", ") + " and " + edits[last]
	}
	return subject + " didn't match this config, so nothing was saved; set allow_partial_edits to save the other edits"
}

func agentConfigEditKey(key, targetID string) string {
	if key == "" {
		return ""
	}
	return "edit:" + key + ":" + targetID
}

func agentConfigEdits(input []openapi.AgentConfigEdit) ([]agentconfig.Edit, error) {
	var edits []agentconfig.Edit
	raw, err := json.Marshal(input)
	if err == nil {
		err = json.Unmarshal(raw, &edits)
	}
	if err != nil {
		return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid edit")
	}
	return edits, nil
}

func encodeAgentConfigEditCursor(cursor, rollout agentConfigEditCursor) (string, error) {
	cursor.Query, cursor.Rollout = rollout.Query, rollout.Rollout
	cursor.Before, cursor.CreatedAt = cursor.Before.UTC(), cursor.CreatedAt.UTC()
	agentID, err := idOrEmpty(publicid.KindAgent, cursor.agentID)
	if err != nil {
		return "", err
	}
	cursor.AgentID = agentID
	payload, err := json.Marshal(cursor)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func decodeAgentConfigEditCursor(raw, query string) (agentConfigEditCursor, error) {
	var cursor agentConfigEditCursor
	if len(raw) > maxCursorTokenLength {
		return cursor, errMalformedCursor
	}
	payload, err := base64.RawURLEncoding.DecodeString(raw)
	if err != nil || len(payload) > maxCursorPayloadLength || json.Unmarshal(payload, &cursor) != nil ||
		cursor.Query != query || cursor.Index < 0 {
		return agentConfigEditCursor{}, errMalformedCursor
	}
	if cursor.AgentID != "" {
		var ok bool
		if cursor.agentID, ok = parseOpenAPIPublicID(publicid.KindAgent, cursor.AgentID); !ok {
			return agentConfigEditCursor{}, errMalformedCursor
		}
	}
	return cursor, nil
}
