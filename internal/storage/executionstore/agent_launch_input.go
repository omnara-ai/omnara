package executionstore

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/internal/dbsqlc"
)

func insertLaunchInitialContentInputTx(
	ctx context.Context,
	tx pgx.Tx,
	agent AgentRecord,
	launchedBy identitystore.PrincipalRecord,
	messageActor *ActorParams,
	message string,
	launchIdempotencyKey string,
) (AgentInputRecord, json.RawMessage, error) {
	contentBlocks := []CreateContentBlockInput{{
		Ordinal:     0,
		BlockKind:   ContentBlockKindText,
		TextContent: message,
	}}
	canonicalContentBlocks, err := marshalAgentInputContentBlocks(contentBlocks)
	if err != nil {
		return AgentInputRecord{}, nil, fmt.Errorf("marshal launch initial input content: %w", err)
	}
	launchActor := messageActor
	if launchActor == nil {
		launchActor, err = OmnaraActorParams(agent.OrgID, launchedBy)
		if err != nil {
			return AgentInputRecord{}, nil, err
		}
	}
	launchActorID, err := resolveActorTx(
		ctx,
		dbsqlc.New(tx),
		agent.ProjectID,
		agent.ID,
		launchActor,
		NilID,
	)
	if err != nil {
		return AgentInputRecord{}, nil, err
	}
	agentInput, err := insertAgentInputTx(ctx, tx, insertAgentInputInput{
		ProjectID:           agent.ProjectID,
		AgentID:             agent.ID,
		DeliveryMode:        DeliveryModeQueued,
		ActorID:             launchActorID,
		IdempotencyScope:    "content_input",
		InputIdempotencyKey: launchChildIdempotencyKey(launchIdempotencyKey, "content-input"),
		Metadata:            json.RawMessage(`{}`),
	})
	if err != nil {
		return AgentInputRecord{}, nil, err
	}
	if err := createAgentInputContentBlocksTx(
		ctx,
		tx,
		agentInput,
		contentBlocks,
	); err != nil {
		return AgentInputRecord{}, nil, err
	}
	return agentInput, canonicalContentBlocks, nil
}

func launchChildIdempotencyKey(parent, child string) string {
	if parent == "" {
		return ""
	}
	return "launch:" + parent + ":" + child
}

func machineSourceSlotChildIdempotencyKey(agentID ID, index, slotIndex int, child string) string {
	return agentChildIdempotencyKey(
		agentID,
		fmt.Sprintf("machine-source:%d:slot:%d:%s", index, slotIndex, child),
	)
}

func agentChildIdempotencyKey(agentID ID, child string) string {
	return "agent:" + agentID.String() + ":" + child
}
