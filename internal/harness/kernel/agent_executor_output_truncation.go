package kernel

import (
	"context"
	"fmt"

	"github.com/omnara-ai/omnara/internal/modelenvelope"
)

const maxConsecutiveOutputContinuations = 2

func (e AgentExecutor) recordTruncatedModelOutput(
	ctx context.Context,
	input ModelWorkExecution,
	step modelStep,
) (modelStep, error) {
	count, err := e.Store.Execution().ConsecutiveOutputContinuations(
		ctx,
		input.ProjectID,
		input.AgentID,
		step.Context.InputEventSequence,
		maxConsecutiveOutputContinuations,
	)
	if err != nil {
		return modelStep{}, err
	}
	continueAfterTruncation := count < maxConsecutiveOutputContinuations
	feedback := "The previous model response reached its output token limit. " +
		"Any tool calls in that response were discarded and did not run. " +
		"Continue the task using smaller tool calls, split large writes into several calls, " +
		"or use a short program to generate repetitive content."
	if !continueAfterTruncation {
		feedback = fmt.Sprintf("The model reached its output token limit %d consecutive times. "+
			"Automatic continuation stopped. Continue with smaller tool calls or increase the output allowance.",
			maxConsecutiveOutputContinuations+1)
	}
	content := append([]modelenvelope.ResponsePart(nil), step.Envelope.Normalized.Content...)
	step.Envelope.Normalized.Content = append(content, modelenvelope.ResponsePart{
		Type: modelenvelope.ResponsePartTypeError, Text: feedback,
	})
	return e.recordSuccessfulModelOutput(ctx, input, step, continueAfterTruncation)
}
