package agentexecution

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/agentmessage"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/internal/agentexecution/internal/executiondb"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
	"github.com/omnara-ai/omnara/internal/storage/internal/lifecyclelock"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

type CreateAgentInput struct {
	ID                      uuid.UUID
	ProjectID               uuid.UUID
	ParentID                uuid.UUID
	ConfigID                uuid.UUID
	ProfileID               uuid.UUID
	Name                    string
	SubagentKey             string
	IdempotencyKey          string
	ArchiveAfterIdleMinutes *int32
}

func (u *Unit) CreateAgent(ctx context.Context, input CreateAgentInput) (*Handle, error) {
	if err := u.active(); err != nil {
		return nil, err
	}
	if input.ID == uuid.Nil || input.ProjectID == uuid.Nil || input.ConfigID == uuid.Nil {
		return nil, errors.New("agent, project and config are required")
	}
	root := input.ID
	if input.ParentID != uuid.Nil {
		parent := u.handles[input.ParentID]
		if parent == nil || parent.route.ProjectID != input.ProjectID {
			return nil, ErrLockPlan
		}
		current, err := executiondb.New().
			ReadExecutionAgentIdentity(ctx,
				u.DB(),
				executiondb.ReadExecutionAgentIdentityParams{ProjectID: input.ProjectID,
					ID: input.ParentID})
		if err != nil {
			return nil, err
		}
		if current.State != "active" {
			return nil, storeerr.ErrStateTransitionConflict
		}
		root = parent.route.RootAgentID
	}
	_, err := executiondb.New().CreateExecutionAgent(ctx, u.DB(), executiondb.CreateExecutionAgentParams{
		ID:        input.ID,
		ProjectID: input.ProjectID,
		RootID:    root,
		ParentID:  nullableID(input.ParentID),
		ConfigID:  input.ConfigID,
		ProfileID: nullableID(
			input.ProfileID,
		),
		Name:                    input.Name,
		SubagentKey:             input.SubagentKey,
		IdempotencyKey:          optionalText(input.IdempotencyKey),
		ArchiveAfterIdleMinutes: input.ArchiveAfterIdleMinutes})
	if err != nil {
		return nil, err
	}
	if err = u.CreatedAgent(input.ProjectID, input.ID, input.ParentID); err != nil {
		u.aborted = true
		return nil, err
	}
	h := u.handles[input.ID]
	h.mutation = &executionMutation{head: ExecutionHead{AgentID: input.ID}, dirty: true, insertHead: true}
	return h, nil
}

func (h *Handle) SetInteractionTarget(
	ctx context.Context,
	id uuid.UUID,
	handler string,
	automatic bool,
) (bool, error) {
	if _, err := h.Route(); err != nil {
		return false, err
	}
	n, err := executiondb.New().
		WriteExecutionInteractionTarget(ctx,
			h.unit.DB(),
			executiondb.WriteExecutionInteractionTargetParams{ID: h.route.AgentID,
				TargetID:   nullableID(id),
				HandlerKey: optionalText(handler),
				AutoSelect: automatic})
	return n > 0, err
}

func (h *Handle) Repair(ctx context.Context) (ExecutionSnapshot, error) {
	snapshot, err := h.ReconstructExecution(ctx)
	if err != nil {
		h.unit.aborted = true
		return ExecutionSnapshot{}, err
	}
	h.mutation = &executionMutation{head: snapshot.Head, loaded: &snapshot, dirty: true, insertHead: true}
	return snapshot, nil
}

func (h *Handle) parentEffect(ctx context.Context, kind, text, key string, interactionID uuid.UUID) error {
	if h.route.RootAgentID == h.route.AgentID {
		return nil
	}
	child, err := executiondb.New().
		ReadExecutionAgentIdentity(ctx,
			h.unit.DB(),
			executiondb.ReadExecutionAgentIdentityParams{ProjectID: h.route.ProjectID,
				ID: h.route.AgentID})
	if err != nil {
		return err
	}
	parent := h.unit.handles[valueOrZero(child.ParentAgentID)]
	if parent == nil {
		return ErrLockPlan
	}
	snapshot, err := parent.LoadExecution(ctx)
	if err != nil {
		return err
	}
	if snapshot.View.State == AgentArchived {
		return nil
	}
	principal, err := publicid.Encode(publicid.KindAgent, h.route.AgentID)
	if err != nil {
		return err
	}
	tenant, err := publicid.Encode(publicid.KindOrganization, child.OrgID)
	if err != nil {
		return err
	}
	q := dbsqlc.New(h.unit.DB())
	actor, err := q.GetActorByIdentity(
		ctx,
		dbsqlc.GetActorByIdentityParams{
			ProjectID:        h.route.ProjectID,
			Provider:         "omnara",
			ProviderTenantID: &tenant,
			ProviderUserID:   principal,
		},
	)
	var actorID uuid.UUID
	if errors.Is(err, pgx.ErrNoRows) {
		created, createErr := q.UpsertActorIdentity(
			ctx,
			dbsqlc.UpsertActorIdentityParams{
				ProjectID:        h.route.ProjectID,
				Provider:         "omnara",
				ProviderTenantID: &tenant,
				ProviderUserID:   principal,
				Metadata:         json.RawMessage(`{}`),
			},
		)
		if createErr != nil {
			return createErr
		}
		actorID = created.ID
	} else if err != nil {
		return err
	} else {
		actorID = actor.ID
	}
	metadata := map[string]any{
		"kind":     kind,
		"agent_id": principal,
		"name":     child.Name,
		"key":      child.SubagentKey,
	}
	if interactionID != uuid.Nil {
		encoded, err := publicid.Encode(publicid.KindAgentInteraction, interactionID)
		if err != nil {
			return err
		}
		metadata["interaction_id"] = encoded
	}
	data, err := json.Marshal(map[string]any{"subagent_message": metadata})
	if err != nil {
		return err
	}
	name := child.Name
	if name == "" {
		name = child.SubagentKey
	}
	if name == "" {
		name = "agent"
	}
	body := agentmessage.Render(name, child.SubagentKey, principal, kind, text)
	_, err = parent.ReceiveContent(
		ctx,
		ReceiveContentInput{
			ActorID:          actorID,
			DeliveryMode:     "steering",
			IdempotencyScope: "subagent_message",
			IdempotencyKey:   key,
			Metadata:         data,
			Content:          []Content{{Kind: "text", Text: body}},
		},
	)
	if err != nil {
		return err
	}
	return err
}

func (h *Handle) questionEffect(ctx context.Context, i Interaction, form interactionform.Form) error {
	return h.parentEffect(
		ctx,
		"question",
		interactionform.RenderQuestion(form),
		"question:"+i.ID.String(),
		i.ID,
	)
}

func (u *Unit) ClearIntegrationTargets(ctx context.Context, projectID, integrationID uuid.UUID) error {
	q := executiondb.New()
	ids, err := q.ListExecutionIntegrationTargetAgents(
		ctx,
		u.DB(),
		executiondb.ListExecutionIntegrationTargetAgentsParams{
			ProjectID:     projectID,
			IntegrationID: integrationID,
		},
	)
	if err != nil {
		return err
	}
	refs := make([]lifecyclelock.AgentRef, len(ids))
	for i, id := range ids {
		refs[i] = lifecyclelock.AgentRef{ProjectID: projectID, AgentID: id}
	}
	if err = u.LockAgentRefs(ctx, refs, LifecycleAuthority{}); err != nil {
		return err
	}
	return q.ClearExecutionIntegrationTargets(
		ctx,
		u.DB(),
		executiondb.ClearExecutionIntegrationTargetsParams{
			ProjectID:     projectID,
			IntegrationID: integrationID,
			AgentIds:      ids,
		},
	)
}
