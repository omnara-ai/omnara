package slack

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

const ActionBodyMaxBytes = 1024 * 1024
const actionResponseTimeout = 2 * time.Second

type ActionsEnvelope struct {
	Type        string `json:"type"`
	APIAppID    string `json:"api_app_id"`
	ResponseURL string `json:"response_url"`
	Team        struct {
		ID string `json:"id"`
	} `json:"team"`
	User    ActionsUser    `json:"user"`
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
	ActionID string `json:"action_id"`
	Value    string `json:"value"`
}

type ActionState struct {
	Values map[string]map[string]actionStateValue `json:"values"`
}

type actionStateOption struct {
	Value string `json:"value"`
}

type actionStateValue struct {
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
	return PromptActionValue{}, errors.New("missing Omnara integration prompt action value")
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
		value.IntegrationTargetID == "" {
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

func ReplaceOriginalActionMessage(
	ctx context.Context,
	client *http.Client,
	responseURL string,
	text string,
) (APIResult, error) {
	if !ValidActionResponseURL(responseURL) {
		return APIResult{PermanentFailure: true, Message: "invalid slack action response URL"}, nil
	}
	body, err := json.Marshal(map[string]any{
		"replace_original": true,
		"text":             text,
	})
	if err != nil {
		return APIResult{}, err
	}
	requestCtx, cancel := context.WithTimeout(ctx, actionResponseTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(
		requestCtx,
		http.MethodPost,
		responseURL,
		bytes.NewReader(body),
	)
	if err != nil {
		return APIResult{PermanentFailure: true, Message: err.Error()}, nil
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	return doActionResponseRequest(client, req)
}

func ValidActionResponseURL(raw string) bool {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return false
	}
	if parsed.Scheme != "https" {
		return false
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "hooks.slack.com" && host != "hooks.slack-gov.com" {
		return false
	}
	return strings.HasPrefix(parsed.EscapedPath(), "/actions/")
}

func doActionResponseRequest(client *http.Client, req *http.Request) (APIResult, error) {
	resp, err := httpClientWithoutRedirects(client).Do(req)
	if err != nil {
		return APIResult{DeliveryUnknown: true, Message: err.Error()}, nil
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := readResponseBody(resp.Body, toolResponseMaxBytes)
	if err != nil {
		return APIResult{DeliveryUnknown: true, Message: err.Error()}, nil
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		if resp.StatusCode >= 500 {
			return APIResult{
				Code:             "transient_failure",
				TransientFailure: true,
				Message:          slackStatusError("slack action response", resp.StatusCode, body).Error(),
			}, nil
		}
		return APIResult{
			Code:             "permanent_failure",
			PermanentFailure: true,
			Message:          slackStatusError("slack action response", resp.StatusCode, body).Error(),
		}, nil
	}
	return APIResult{}, nil
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
