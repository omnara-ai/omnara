package slack

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"strconv"
	"strings"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

const ActionBodyMaxBytes = 1024 * 1024

type ActionsEnvelope struct {
	Type     string `json:"type"`
	APIAppID string `json:"api_app_id"`
	Team     struct {
		ID string `json:"id"`
	} `json:"team"`
	User    ActionsUser `json:"user"`
	Channel struct {
		ID string `json:"id"`
	} `json:"channel"`
	Message struct {
		TS       string         `json:"ts"`
		ThreadTS string         `json:"thread_ts"`
		Blocks   []HistoryBlock `json:"blocks"`
	} `json:"message"`
	Container struct {
		ChannelID string `json:"channel_id"`
		MessageTS string `json:"message_ts"`
	} `json:"container"`
	Actions []actionButton `json:"actions"`
	State   ActionState    `json:"state"`
}

type ActionsUser struct {
	ID       string `json:"id"`
	TeamID   string `json:"team_id"`
	Name     string `json:"name"`
	Username string `json:"username"`
}

type actionButton struct {
	Type            string              `json:"type"`
	ActionID        string              `json:"action_id"`
	Value           string              `json:"value"`
	SelectedOption  *actionStateOption  `json:"selected_option,omitempty"`
	SelectedOptions []actionStateOption `json:"selected_options,omitempty"`
}

type ActionState struct {
	Values map[string]map[string]actionStateValue `json:"values"`
}

type actionStateOption struct {
	Value string `json:"value"`
}

type actionStateValue struct {
	Value           string              `json:"value"`
	SelectedOption  *actionStateOption  `json:"selected_option"`
	SelectedOptions []actionStateOption `json:"selected_options"`
}

func DecodeActionsEnvelope(raw []byte) (ActionsEnvelope, error) {
	values, err := url.ParseQuery(string(raw))
	if err != nil {
		return ActionsEnvelope{}, errors.New("invalid slack action form payload")
	}
	payload := values.Get("payload")
	if payload == "" {
		return ActionsEnvelope{}, errors.New("missing slack action payload")
	}
	var envelope ActionsEnvelope
	decoder := json.NewDecoder(strings.NewReader(payload))
	if err := decoder.Decode(&envelope); err != nil {
		return ActionsEnvelope{}, errors.New("invalid slack action payload")
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return ActionsEnvelope{}, errors.New("invalid slack action payload")
	}
	if envelope.Type != "block_actions" {
		return ActionsEnvelope{}, errors.New("unsupported slack action payload type")
	}
	if envelope.User.ID == "" {
		return ActionsEnvelope{}, errors.New("slack action is missing user")
	}
	if len(envelope.Actions) == 0 {
		return ActionsEnvelope{}, errors.New("slack action is missing action")
	}
	return envelope, nil
}

func ValidateActionIdentity(identity Identity, envelope ActionsEnvelope) bool {
	if envelope.APIAppID != identity.AppID || envelope.Team.ID != identity.WorkspaceID {
		return false
	}
	if envelope.User.TeamID != "" && envelope.User.TeamID != identity.WorkspaceID {
		return false
	}
	return true
}

func PromptActionFromActions(envelope ActionsEnvelope) (PromptActionValue, error) {
	for _, action := range envelope.Actions {
		if !PromptActionID(action.ActionID) || action.Value == "" {
			continue
		}
		value, err := decodePromptActionValue(action.Value)
		if err != nil {
			continue
		}
		return value, nil
	}
	return PromptActionValue{}, errors.New("missing Omnara app prompt action value")
}

func PromptCallbackInteractionID(envelope ActionsEnvelope) (string, error) {
	// Form edits lack a Submit action, so their prompt identity comes from the message marker.
	if action, err := PromptActionFromActions(envelope); err == nil {
		return action.InteractionID, nil
	}
	for _, block := range envelope.Message.Blocks {
		if id, ok := strings.CutPrefix(block.BlockID, promptMarkerBlockID("")); ok && id != "" {
			return id, nil
		}
	}
	return "", errors.New("missing Omnara app prompt identity")
}

func PromptActionID(actionID string) bool {
	return actionID == PromptAction || strings.HasPrefix(actionID, PromptAction+"_")
}

func decodePromptActionValue(raw string) (PromptActionValue, error) {
	var value PromptActionValue
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&value); err != nil {
		return PromptActionValue{}, fmt.Errorf("invalid action value: %w", err)
	}
	if err := rejectTrailingJSON(decoder); err != nil {
		return PromptActionValue{}, fmt.Errorf("invalid action value: %w", err)
	}
	if value.Type != PromptType || value.InteractionID == "" || value.AgentID == "" ||
		value.AppTargetID == "" {
		return PromptActionValue{}, errors.New("incomplete action value")
	}
	return value, nil
}

type InteractionFormResolutionResult struct {
	Resolution    interactionform.Resolution
	InvalidReason string
}

func ResolveInteractionForm(
	value interactionform.Form,
	state ActionState,
) InteractionFormResolutionResult {
	resolution := interactionform.Resolution{
		Answers: make([]interactionform.Answer, 0, len(value.Questions)),
	}
	for index, question := range value.Questions {
		block, ok := state.Values[questionBlockID(index)]
		if !ok {
			return InteractionFormResolutionResult{
				InvalidReason: fmt.Sprintf("question %d requires an answer", index),
			}
		}
		stateValue := block[PromptAnswerAction]
		optionIndices := make([]int, 0, 1)
		if question.Multiple {
			for _, selected := range stateValue.SelectedOptions {
				optionIndex, err := strconv.Atoi(strings.TrimSpace(selected.Value))
				if err != nil {
					return InteractionFormResolutionResult{
						InvalidReason: fmt.Sprintf("question %d has an invalid option", index),
					}
				}
				optionIndices = append(optionIndices, optionIndex)
			}
		} else if stateValue.SelectedOption != nil {
			optionIndex, err := strconv.Atoi(strings.TrimSpace(stateValue.SelectedOption.Value))
			if err != nil {
				return InteractionFormResolutionResult{
					InvalidReason: fmt.Sprintf("question %d has an invalid option", index),
				}
			}
			optionIndices = append(optionIndices, optionIndex)
		}
		if len(optionIndices) == 0 {
			return InteractionFormResolutionResult{
				InvalidReason: fmt.Sprintf("question %d requires an answer", index),
			}
		}
		resolution.Answers = append(resolution.Answers, interactionform.Answer{
			OptionIndices: optionIndices,
			Text:          state.Values[questionBlockID(index)+"_text"][PromptAnswerAction].Value,
		})
	}
	normalized, err := interactionform.NormalizeResolution(value, resolution)
	if err != nil {
		return InteractionFormResolutionResult{InvalidReason: err.Error()}
	}
	return InteractionFormResolutionResult{Resolution: normalized}
}

func (user ActionsUser) DisplayName() string {
	if name := strings.TrimSpace(user.Name); name != "" {
		return name
	}
	return strings.TrimSpace(user.Username)
}

func rejectTrailingJSON(decoder *json.Decoder) error {
	var extra struct{}
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return errors.New("trailing data")
		}
		return err
	}
	return nil
}
