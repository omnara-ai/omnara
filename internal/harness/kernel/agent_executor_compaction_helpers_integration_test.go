//go:build integration

package kernel

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
)

func (f kernelFixture) admitSteeringInputsTurn(
	t *testing.T,
	ctx context.Context,
	agentID, userID storage.ID,
	texts []string,
	now time.Time,
) ModelWorkExecution {
	t.Helper()
	if len(texts) == 0 {
		t.Fatal("at least one steering input is required")
	}
	inputs := make([]executionstore.AgentInputRecord, 0, len(texts))
	for index, text := range texts {
		input, _, _, err := f.Store.Execution().CreateAgentContentInput(ctx, executionstore.CreateAgentContentInputInput{
			ProjectID:      kernelTestProjectID,
			AgentID:        agentID,
			Actor:          kernelTestOmnaraActorParams(t, userID),
			ContentBlocks:  mustKernelJSON([]map[string]string{{"type": "text", "text": text}}),
			DeliveryMode:   executionstore.DeliveryModeSteering,
			IdempotencyKey: "kernel-steering-input-" + agentID.String() + "-" + text,
		})
		if err != nil {
			t.Fatalf("create steering input %d: %v", index, err)
		}
		inputs = append(inputs, input)
	}
	claim, found, err := f.Store.Execution().ClaimNextAgentWork(
		ctx,
		kernelTestClaimInput(now.Add(time.Second+time.Millisecond)),
	)
	if err != nil {
		t.Fatalf("claim steering input work: %v", err)
	}
	if !found || claim.Kind != executionstore.AgentWorkModel || len(claim.Model.InputIDs) != len(inputs) {
		t.Fatalf("claimed steering inputs found=%v claim=%+v want %d inputs", found, claim, len(inputs))
	}
	for index := range inputs {
		if claim.Model.InputIDs[index] != inputs[index].ID {
			t.Fatalf("claimed input %d = %s, want %s", index, claim.Model.InputIDs[index], inputs[index].ID)
		}
	}
	return modelWorkExecutionFromClaimForKernelTest(
		claim,
		now.Add(time.Second+2*time.Millisecond),
	)
}

func completeProgressiveSummaryResponse(summary string) model.Response {
	return model.Response{
		ID:         "resp_" + strings.ReplaceAll(summary, " ", "_"),
		Content:    []model.ResponsePart{{Type: "text", Text: summary}},
		StopReason: model.StopReasonEndTurn,
	}
}

func intPtrForKernelCompactionTest(value int) *int {
	return &value
}
