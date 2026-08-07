package tools

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/toolcatalog"
)

type askQuestionInput struct {
	Questions []askQuestionInputQuestion `json:"questions"`
}

type askQuestionInputQuestion struct {
	Prompt   string                   `json:"prompt"`
	Multiple bool                     `json:"multiple,omitempty"`
	Options  []askQuestionInputOption `json:"options"`
}

type askQuestionInputOption struct {
	Label string `json:"label"`
}

func validateQuestionInput(input json.RawMessage) error {
	_, err := askQuestionForm(input)
	return err
}

func prepareStructuredQuestion(
	ctx context.Context,
	call transactionalToolContext,
) (transactionalPhaseResult, error) {
	value, err := askQuestionForm(call.Call.Input)
	if err != nil {
		return nil, err
	}
	return executeInTransaction(
		executionstore.CreateQuestionForToolCall(
			executionstore.CreateQuestionInteractionInput{
				Form: value,
			},
		),
		nil,
	), nil
}

func deliverStructuredQuestionPrompts(
	ctx context.Context,
	call asyncToolContext,
) (asyncPhaseResult, error) {
	interaction, found, err := call.Executor.Store.Execution().GetAgentInteractionByToolCallKind(
		ctx,
		call.Turn.ProjectID,
		call.Turn.AgentID,
		call.ToolCallID,
		executionstore.AgentInteractionKindQuestion,
	)
	if err != nil {
		return nil, fmt.Errorf("load question interaction for prompt delivery: %w", err)
	}
	if !found {
		return nil, errors.New("question interaction not found")
	}
	if interaction.State == executionstore.AgentInteractionStateOpen {
		if err := call.Executor.postIntegrationPrompt(ctx, call.Turn, interaction); err != nil {
			return nil, fmt.Errorf("deliver question interaction: %w", err)
		}
	}
	return awaitDurableAsynchronously(), nil
}

func askQuestionForm(raw json.RawMessage) (interactionform.Form, error) {
	var input askQuestionInput
	if err := decodeSingleStrictJSON(raw, &input, "ask_question input"); err != nil {
		return interactionform.Form{}, fmt.Errorf("decode ask_question input: %w", err)
	}
	if len(input.Questions) > toolcatalog.MaxAskQuestions {
		return interactionform.Form{}, fmt.Errorf(
			"ask_question accepts at most %d questions",
			toolcatalog.MaxAskQuestions,
		)
	}
	questions := make([]interactionform.Question, 0, len(input.Questions))
	for _, question := range input.Questions {
		options := make([]interactionform.Option, 0, len(question.Options)+1)
		for _, option := range question.Options {
			options = append(options, interactionform.Option{Label: option.Label})
		}
		options = append(options, interactionform.Option{Label: "Other", AllowsText: true})
		questions = append(questions, interactionform.Question{
			Prompt:   question.Prompt,
			Multiple: question.Multiple,
			Options:  options,
		})
	}
	title := "Questions"
	if len(questions) == 1 {
		title = "Question"
	}
	value, err := interactionform.New(title, nil, questions)
	if err != nil {
		return interactionform.Form{}, fmt.Errorf("ask_question: %w", err)
	}
	return value, nil
}
