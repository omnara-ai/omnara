package discord

import (
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/interactionform"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func TestInteractionFormChoicesModalAndText(t *testing.T) {
	t.Parallel()
	id, err := publicid.Encode(publicid.KindAgentInteraction, uuid.New())
	require.NoError(t, err)
	form := interactionform.Form{
		Title: "Choose",
		Questions: []interactionform.Question{
			{Prompt: "Ship?", Options: []interactionform.Option{{Label: "Yes"}, {Label: "Other", AllowsText: true}}},
		},
	}
	_, rows := InteractionFormPrompt(form, id)
	require.Len(t, rows, 1)
	response, answer, err := ResolveInteractionForm(
		form,
		Interaction{Type: 3, Data: InteractionData{CustomID: rows[0].Components[0].CustomID}},
	)
	require.NoError(t, err)
	require.Equal(t, 6, response.Type)
	require.Equal(t, []int{0}, answer.Answers[0].OptionIndices)
	button := rows[0].Components[1].CustomID
	modal, answer, err := ResolveInteractionForm(form, Interaction{Type: 3, Data: InteractionData{CustomID: button}})
	require.NoError(t, err)
	require.Nil(t, answer)
	require.Equal(t, 9, modal.Type)
	require.True(t, validResponse(3, modal))
	encoded, err := json.Marshal(modal)
	require.NoError(t, err)
	require.Contains(t, string(encoded), `"required":false`, "Discord defaults an omitted required field to true")
	_, empty, err := ResolveInteractionForm(
		form,
		Interaction{Type: 5, Data: InteractionData{CustomID: modal.Data.CustomID,
			Components: json.RawMessage(`[{"components":[{"custom_id":"q0","value":""}]}]`)}},
	)
	require.NoError(t, err)
	require.Equal(t, []int{1}, empty.Answers[0].OptionIndices)
	_, answer, err = ResolveInteractionForm(
		form,
		Interaction{Type: 5, Data: InteractionData{CustomID: modal.Data.CustomID,
			Components: json.RawMessage(`[{"components":[{"custom_id":"q0","value":"later"}]}]`)}},
	)
	require.NoError(t, err)
	require.Equal(t, "later", answer.Answers[0].Text)
	require.Equal(t, []int{1}, answer.Answers[0].OptionIndices)
}

func TestInteractionFormMultipleQuestionsAndInvalidAnswers(t *testing.T) {
	t.Parallel()
	id, err := publicid.Encode(publicid.KindAgentInteraction, uuid.New())
	require.NoError(t, err)
	form := interactionform.Form{Title: "Review", Questions: []interactionform.Question{
		{
			Prompt:   "Which?",
			Multiple: true,
			Options:  []interactionform.Option{{Label: "A"}, {Label: "B", AllowsText: true}},
		},
		{Prompt: "Ready?", Options: []interactionform.Option{{Label: "Yes"}, {Label: "No"}}},
	}}
	_, rows := InteractionFormPrompt(form, id)
	modal, answer, err := ResolveInteractionForm(
		form,
		Interaction{Type: 3, Data: InteractionData{CustomID: rows[0].Components[0].CustomID}},
	)
	require.NoError(t, err)
	require.Nil(t, answer)
	require.True(t, validResponse(3, modal))
	_, answer, err = ResolveInteractionForm(
		form,
		Interaction{Type: 5, Data: InteractionData{
			CustomID: modal.Data.CustomID,
			Components: json.RawMessage(
				`[{"components":[{"custom_id":"q0","value":"1,2: explanation"}]},{"components":[{"custom_id":"q1","value":"1"}]}]`,
			),
		}},
	)
	require.NoError(t, err)
	require.Equal(t, []int{0, 1}, answer.Answers[0].OptionIndices)
	require.Equal(t, "explanation", answer.Answers[0].Text)
	for _, raw := range []string{
		`[]`,
		`[{"components":[{"custom_id":"q0","value":"1: forbidden text"}]},{"components":[{"custom_id":"q1","value":"1"}]}]`,
		`[{"components":[{"custom_id":"q0","value":"1,1"}]},{"components":[{"custom_id":"q1","value":"3"}]}]`,
	} {
		_, _, err := ResolveInteractionForm(
			form,
			Interaction{
				Type: 5,
				Data: InteractionData{CustomID: modal.Data.CustomID, Components: json.RawMessage(raw)},
			},
		)
		require.Error(t, err)
	}
}
