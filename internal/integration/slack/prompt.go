package slack

import "strconv"

const (
	PromptType         = "omnara_interaction"
	PromptAction       = "omnara_interaction"
	PromptAnswerAction = "omnara_answer"
)

type PromptActionValue struct {
	Type                string `json:"type"`
	InteractionID       string `json:"interaction_id"`
	AgentID             string `json:"agent_id"`
	IntegrationTargetID string `json:"integration_target_id"`
}

func questionBlockID(index int) string {
	return "omnara_question_" + strconv.Itoa(index)
}
