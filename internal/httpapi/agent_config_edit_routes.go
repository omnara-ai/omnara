package httpapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"runtime/debug"
	"slices"
	"strconv"
	"strings"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/agentconfig"
	"github.com/omnara-ai/omnara/internal/agentconfigcompile"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
	logpkg "github.com/omnara-ai/omnara/observability/wideevent"
	"golang.org/x/sync/errgroup"
)

const (
	maxAgentConfigEditTargets  = 500
	agentConfigEditConcurrency = 4
)

type agentConfigEditRequest struct {
	project      identitystore.ProjectRecord
	principal    identitystore.PrincipalRecord
	edits        []agentconfig.Edit
	allowPartial bool
	rolloutKey   string
}

type agentConfigEditTarget struct {
	profile bool
	id      uuid.UUID
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
	edits, err := agentConfigEdits(body.Edits)
	if err != nil {
		return nil, err
	}
	edit := &agentConfigEditRequest{
		project: scope.project, principal: principal, edits: edits,
		allowPartial: body.OnFailedEdit == openapi.OnFailedEditIgnore,
	}
	if request.Params.IdempotencyKey != nil && *request.Params.IdempotencyKey != "" {
		edit.rolloutKey = shortHash(*request.Params.IdempotencyKey)
	}
	targets, err := s.resolveAgentConfigEditTargets(ctx, edit, body.Targets)
	if err != nil {
		return nil, err
	}
	results, err := s.editAgentConfigTargets(ctx, edit, targets, body.OnFailedEdit == openapi.OnFailedEditStop)
	if err != nil {
		return nil, err
	}
	for _, result := range results {
		if result.Status != openapi.AgentConfigEditResultStatusUpdated && result.Edits != nil {
			for i, outcome := range *result.Edits {
				if outcome == openapi.AgentConfigEditResultEditsApplied {
					(*result.Edits)[i] = openapi.AgentConfigEditResultEditsNotApplied
				}
			}
		}
	}
	return openapi.EditAgentConfigs200JSONResponse{Data: results}, nil
}

func (s strictOpenAPIServer) editAgentConfigTargets(
	ctx context.Context,
	edit *agentConfigEditRequest,
	targets []agentConfigEditTarget,
	stop bool,
) ([]openapi.AgentConfigEditResult, error) {
	results := make([]openapi.AgentConfigEditResult, len(targets))
	deleteMachines := make([][]executionstore.MachineRecord, len(targets))
	defer func() { s.server.startPoolMachineDeletion(ctx, slices.Concat(deleteMachines...)) }()
	if stop {
		failed := false
		for i, target := range targets {
			var err error
			if failed {
				results[i], _, err = agentConfigEditResult(target)
				results[i].Status = openapi.AgentConfigEditResultStatusSkipped
			} else {
				results[i], err = s.editAgentConfigTarget(ctx, edit, target, &deleteMachines[i])
				failed = results[i].Status == openapi.AgentConfigEditResultStatusFailed
			}
			if err != nil {
				return nil, err
			}
		}
		return results, nil
	}
	var group errgroup.Group
	group.SetLimit(agentConfigEditConcurrency)
	for i, target := range targets {
		group.Go(func() (err error) {
			results[i], err = s.editAgentConfigTarget(ctx, edit, target, &deleteMachines[i])
			return err
		})
	}
	if err := group.Wait(); err != nil {
		return nil, err
	}
	return results, nil
}

func (s strictOpenAPIServer) resolveAgentConfigEditTargets(
	ctx context.Context,
	edit *agentConfigEditRequest,
	input []openapi.AgentConfigEditTarget,
) ([]agentConfigEditTarget, error) {
	var profiles, agents, instances []uuid.UUID
	var profileKeys []string
	for _, item := range input {
		kind, err := item.Discriminator()
		if err != nil {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid target")
		}
		switch kind {
		case string(openapi.AgentConfigEditProfileTargetTypeProfile):
			target, err := item.AsAgentConfigEditProfileTarget()
			if err != nil {
				return nil, err
			}
			id, ok := parseOpenAPIPublicID(publicid.KindAgentProfile, target.AgentProfileId)
			if !ok {
				return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent_profile_id")
			}
			applyToProfile := target.ApplyToProfile == nil || *target.ApplyToProfile
			if !applyToProfile && !target.ApplyToInstances {
				return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest,
					"apply_to_profile and apply_to_instances can't both be false")
			}
			if applyToProfile && !slices.Contains(profiles, id) {
				profiles = append(profiles, id)
				if key := agentConfigEditKey(edit.rolloutKey, target.AgentProfileId); key != "" {
					profileKeys = append(profileKeys, key)
				}
			}
			if target.ApplyToInstances && !slices.Contains(instances, id) {
				instances = append(instances, id)
			}
		case string(openapi.AgentConfigEditAgentTargetTypeAgent):
			target, err := item.AsAgentConfigEditAgentTarget()
			if err != nil {
				return nil, err
			}
			id, ok := parseOpenAPIPublicID(publicid.KindAgent, target.AgentId)
			if !ok {
				return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid agent_id")
			}
			agents = append(agents, id)
		default:
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, "invalid target")
		}
	}
	store := s.server.store.Execution()
	listed := slices.Clone(profiles)
	for _, id := range instances {
		if !slices.Contains(listed, id) {
			listed = append(listed, id)
		}
	}
	check, err := store.CheckAgentConfigEditTargets(ctx, edit.project.ID, listed, profileKeys)
	if err != nil {
		return nil, err
	}
	targets := make([]agentConfigEditTarget, 0, len(listed)+len(agents))
	for _, id := range listed {
		if !slices.Contains(check.Profiles, id) || slices.Contains(profiles, id) {
			targets = append(targets, agentConfigEditTarget{profile: true, id: id})
		}
	}
	seen := make(map[uuid.UUID]bool, len(agents))
	for _, id := range agents {
		targets, seen[id] = append(targets, agentConfigEditTarget{id: id}), true
	}
	for _, profileID := range instances {
		if !slices.Contains(check.Profiles, profileID) {
			continue
		}
		ids, err := store.ListAgentConfigEditProfileAgentIDs(ctx, edit.project.ID, profileID,
			executionstore.AgentConfigEditProfileAgentsWindow{CreatedBefore: check.Cutoff}, maxAgentConfigEditTargets+1)
		if err != nil {
			return nil, err
		}
		for _, id := range ids {
			if !seen[id] {
				targets, seen[id] = append(targets, agentConfigEditTarget{id: id}), true
			}
		}
		if len(targets) > maxAgentConfigEditTargets {
			return nil, apierror.FromCode(openapi.ErrorCodeInvalidRequest, fmt.Sprintf(
				"targets expand to more than %d profiles and agents", maxAgentConfigEditTargets))
		}
	}
	return targets, nil
}

func agentConfigEditResult(target agentConfigEditTarget) (openapi.AgentConfigEditResult, string, error) {
	var result openapi.AgentConfigEditResult
	if target.profile {
		id, err := publicID(publicid.KindAgentProfile, target.id)
		if err == nil {
			err = result.Target.FromAgentConfigEditProfileTarget(openapi.AgentConfigEditProfileTarget{
				Type: openapi.AgentConfigEditProfileTargetTypeProfile, AgentProfileId: id,
			})
		}
		return result, id, err
	}
	id, err := publicID(publicid.KindAgent, target.id)
	if err == nil {
		err = result.Target.FromAgentConfigEditAgentTarget(openapi.AgentConfigEditAgentTarget{
			Type: openapi.AgentConfigEditAgentTargetTypeAgent, AgentId: id,
		})
	}
	return result, id, err
}

func (s strictOpenAPIServer) editAgentConfigTarget(
	ctx context.Context,
	edit *agentConfigEditRequest,
	target agentConfigEditTarget,
	deleteMachines *[]executionstore.MachineRecord,
) (result openapi.AgentConfigEditResult, err error) {
	result, id, err := agentConfigEditResult(target)
	if err != nil {
		return openapi.AgentConfigEditResult{}, err
	}
	defer func() {
		if recovered := recover(); recovered != nil {
			logpkg.Attach(ctx, logpkg.Fields{"error.stack": string(debug.Stack())})
			result, err = agentConfigEditFailed(ctx, result, fmt.Errorf("panicked: %v", recovered))
		}
	}()
	key := agentConfigEditKey(edit.rolloutKey, id)
	if target.profile {
		return s.editAgentProfileConfig(ctx, edit, target.id, key, result)
	}
	return s.editAgentConfig(ctx, edit, target.id, key, result, deleteMachines)
}

func (s strictOpenAPIServer) editAgentConfig(
	ctx context.Context,
	edit *agentConfigEditRequest,
	agentID uuid.UUID,
	key string,
	result openapi.AgentConfigEditResult,
	deleteMachines *[]executionstore.MachineRecord,
) (openapi.AgentConfigEditResult, error) {
	store := s.server.store.Execution()
	agent, err := store.GetAgentInProject(ctx, edit.project.ID, agentID)
	if err != nil {
		return agentConfigEditFailed(ctx, result, err)
	}
	if key != "" {
		configID, machines, found, err := store.AgentConfigChangeByIdempotencyKey(ctx, edit.project.ID, agentID, key)
		if err != nil {
			return agentConfigEditFailed(ctx, result, err)
		}
		if found {
			*deleteMachines = machines
			return agentConfigEditUpdated(result, configID, configID)
		}
	}
	if agent.State != executionstore.AgentStateActive {
		return agentConfigEditFailed(ctx, result,
			apierror.FromCode(openapi.ErrorCodeStateTransitionConflict, "agent is archived"))
	}
	config, found, err := store.GetAgentConfig(ctx, edit.project.ID, agent.CurrentConfigID)
	if err == nil && !found {
		err = storeerr.ErrNotFound
	}
	if err != nil {
		return agentConfigEditFailed(ctx, result, err)
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
		return agentConfigEditFailed(ctx, result, err)
	}
	*deleteMachines = change.DeleteMachines
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
	key string,
	result openapi.AgentConfigEditResult,
) (openapi.AgentConfigEditResult, error) {
	store := s.server.store.Execution()
	replay := func() (openapi.AgentConfigEditResult, bool, error) {
		configID, generation, found, err := store.AgentProfileVersionByIdempotencyKey(ctx, edit.project.ID, profileID, key)
		if err != nil {
			failed, err := agentConfigEditFailed(ctx, result, err)
			return failed, true, err
		}
		if !found {
			return openapi.AgentConfigEditResult{}, false, nil
		}
		replayed := result
		replayed.Edits, replayed.CurrentGeneration = nil, &generation
		replayed, err = agentConfigEditUpdated(replayed, configID, configID)
		return replayed, true, err
	}
	profile, err := store.GetAgentProfile(ctx, edit.project.ID, profileID)
	if err != nil {
		return agentConfigEditFailed(ctx, result, err)
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
		return agentConfigEditFailed(ctx, result, err)
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
		return agentConfigEditFailed(ctx, result, err)
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
			*result, err = agentConfigEditFailed(ctx, *result, err)
			return agentconfigcompile.Body{}, false, err
		}
		source = generated
	}
	edited, err := agentconfig.ApplyEdits(format, source, edit.edits)
	if err != nil {
		*result, err = agentConfigEditFailed(ctx, *result,
			apierror.FromCode(openapi.ErrorCodeInvalidRequest, err.Error()))
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
		*result, err = agentConfigEditFailed(ctx, *result,
			apierror.FromCode(openapi.ErrorCodeEditConflict, agentConfigEditConflictMessage(missed)))
		return agentconfigcompile.Body{}, false, err
	}
	compiled, err := agentconfigcompile.Compile(
		ctx, s.server.store, edit.project.OrgID, edit.project.ID, s.server.agentConfigOptions, format, edited.Source,
	)
	if err != nil {
		*result, err = agentConfigEditFailed(ctx, *result, agentConfigCompileError(err))
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

func agentConfigEditFailed(
	ctx context.Context,
	result openapi.AgentConfigEditResult,
	err error,
) (openapi.AgentConfigEditResult, error) {
	var responseErr apierror.ResponseError
	if !errors.As(err, &responseErr) {
		responseErr = apierror.ProjectScoped(err)
	}
	if responseErr.Status >= http.StatusInternalServerError {
		if ctx.Err() != nil {
			return openapi.AgentConfigEditResult{}, err
		}
		logpkg.Error(ctx, fmt.Errorf("edit agent config target: %w", err))
		logpkg.Escalate(ctx, logpkg.ErrorLevel)
		result.Edits, result.CurrentGeneration = nil, nil
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
	return subject + " didn't match this config, so nothing was saved; " +
		"set on_failed_edit to ignore to save the other edits"
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
