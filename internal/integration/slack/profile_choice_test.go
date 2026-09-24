package slack

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/stretchr/testify/require"
)

func profileChoiceTestID(t *testing.T) string {
	t.Helper()
	id, err := publicid.Encode(publicid.KindIntegrationProfileChoice, uuid.New())
	require.NoError(t, err)
	return id
}

func TestProfileChoicePromptNativeSelectRoundTrip(t *testing.T) {
	t.Parallel()
	for _, count := range []int{2, 16} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			t.Parallel()
			choiceID := profileChoiceTestID(t)
			options := make([]ProfileChoiceOption, count)
			for i := range options {
				options[i] = ProfileChoiceOption{Key: fmt.Sprintf("slot-%d", i), Name: strings.Repeat("🦉", 90)}
			}
			text, blocks, err := ProfileChoicePrompt(choiceID, options, "Available until 12:00 UTC.")
			require.NoError(t, err)
			require.Contains(t, text, "Choose a profile before continuing")
			require.Contains(t, text, "original request")
			require.Contains(t, text, "Anyone with access to this conversation can choose")
			require.Contains(t, text, "Available until 12:00 UTC.")
			payload, err := PromptPayload(MessageTarget{Channel: "C123", ThreadTS: "123.456"}, text, blocks)
			require.NoError(t, err)
			var message struct {
				Channel  string `json:"channel"`
				ThreadTS string `json:"thread_ts"`
				Blocks   []struct {
					Type     string `json:"type"`
					Elements []struct {
						Type          string `json:"type"`
						ActionID      string `json:"action_id"`
						InitialOption any    `json:"initial_option"`
						Options       []struct {
							Text struct {
								Type string `json:"type"`
								Text string `json:"text"`
							} `json:"text"`
							Value string `json:"value"`
						} `json:"options"`
					} `json:"elements"`
				} `json:"blocks"`
			}
			require.NoError(t, json.Unmarshal(payload, &message))
			require.Equal(t, "C123", message.Channel)
			require.Equal(t, "123.456", message.ThreadTS)
			require.Len(t, message.Blocks, 2)
			require.Equal(t, "actions", message.Blocks[1].Type)
			require.Len(t, message.Blocks[1].Elements, 1)
			menu := message.Blocks[1].Elements[0]
			require.Equal(t, "static_select", menu.Type)
			require.Equal(t, ProfileChoiceActionPrefix+choiceID, menu.ActionID)
			require.False(t, PromptActionID(menu.ActionID))
			require.Nil(t, menu.InitialOption)
			require.LessOrEqual(t, len(menu.ActionID), 255)
			require.Len(t, menu.Options, count)
			for i, option := range menu.Options {
				require.Equal(t, "plain_text", option.Text.Type)
				require.True(t, utf8.ValidString(option.Text.Text))
				require.Equal(t, 75, utf8.RuneCountInString(option.Text.Text))
				require.Equal(t, options[i].Key, option.Value)
			}
			callback := fmt.Sprintf(`{"type":"block_actions","user":{"id":"U123"},"actions":[
{"type":"static_select","action_id":%q,
"selected_option":{"text":{"type":"plain_text","text":"Ignored label"},"value":%q}}
]}`, menu.ActionID, options[count-1].Key)
			envelope, err := DecodeActionsEnvelope([]byte(url.Values{"payload": {callback}}.Encode()))
			require.NoError(t, err)
			selection, err := ProfileChoiceFromActions(envelope)
			require.NoError(t, err)
			require.Equal(t, ProfileChoiceSelection{ChoiceID: choiceID, Key: options[count-1].Key}, selection)
			_, err = PromptActionFromActions(envelope)
			require.Error(t, err, "profile selection cannot resolve an agent interaction")
		})
	}
}

func TestProfileChoicePromptRejectsInvalidMenu(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"zero", "one", "seventeen", "duplicate", "empty key", "long key", "padded key", "nul key",
		"invalid key utf8", "empty name", "invalid name utf8", "wrong identity", "invalid expiry utf8",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			id, expiry := profileChoiceTestID(t), ""
			options := []ProfileChoiceOption{{Key: "one", Name: "First"}, {Key: "two", Name: "Second"}}
			switch scenario {
			case "zero":
				options = nil
			case "one":
				options = options[:1]
			case "seventeen":
				options = make([]ProfileChoiceOption, 17)
			case "duplicate":
				options[1].Key = options[0].Key
			case "empty key":
				options[0].Key = ""
			case "long key":
				options[0].Key = strings.Repeat("x", 65)
			case "padded key":
				options[0].Key = " one"
			case "nul key":
				options[0].Key = "o\x00ne"
			case "invalid key utf8":
				options[0].Key = string([]byte{255})
			case "empty name":
				options[0].Name = "  "
			case "invalid name utf8":
				options[0].Name = string([]byte{255})
			case "wrong identity":
				id = strings.Replace(id, "ipc_", "aint_", 1)
			case "invalid expiry utf8":
				expiry = string([]byte{255})
			}
			_, _, err := ProfileChoicePrompt(id, options, expiry)
			require.Error(t, err)
		})
	}
	options := []ProfileChoiceOption{{Key: strings.Repeat("界", 21), Name: "First"}, {Key: "two", Name: "Second"}}
	text, _, err := ProfileChoicePrompt(profileChoiceTestID(t), options, "")
	require.NoError(t, err)
	require.NotContains(t, strings.ToLower(text), "expir")
	text, _, err = ProfileChoicePrompt(profileChoiceTestID(t), options, strings.Repeat("界", 4000))
	require.NoError(t, err)
	require.True(t, utf8.ValidString(text))
	require.LessOrEqual(t, utf8.RuneCountInString(text), 3000)
}

func TestProfileChoiceFromActionsRejectsMalformedSelections(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"missing", "multiple actions", "wrong envelope", "button", "multi select", "no option", "empty key",
		"long key", "padded key", "invalid key utf8", "multiple values", "button value", "wrong namespace",
		"wrong identity", "extra authority", "nil identity",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			id := profileChoiceTestID(t)
			action := actionButton{Type: "static_select", ActionID: ProfileChoiceActionPrefix + id,
				SelectedOption: &actionStateOption{Value: "one"}}
			envelope := ActionsEnvelope{Type: "block_actions", Actions: []actionButton{action}}
			switch scenario {
			case "missing":
				envelope.Actions = nil
			case "multiple actions":
				envelope.Actions = append(envelope.Actions, action)
			case "wrong envelope":
				envelope.Type = "view_submission"
			case "button":
				envelope.Actions[0].Type = "button"
			case "multi select":
				envelope.Actions[0].Type = "multi_static_select"
			case "no option":
				envelope.Actions[0].SelectedOption = nil
			case "empty key":
				action.SelectedOption.Value = ""
			case "long key":
				action.SelectedOption.Value = strings.Repeat("x", 65)
			case "padded key":
				action.SelectedOption.Value = "one "
			case "invalid key utf8":
				action.SelectedOption.Value = string([]byte{255})
			case "multiple values":
				envelope.Actions[0].SelectedOptions = []actionStateOption{{Value: "one"}, {Value: "two"}}
			case "button value":
				envelope.Actions[0].Value = "one"
			case "wrong namespace":
				envelope.Actions[0].ActionID = PromptAction
			case "wrong identity":
				envelope.Actions[0].ActionID = ProfileChoiceActionPrefix + "not-a-choice"
			case "extra authority":
				envelope.Actions[0].ActionID += ":profile-id"
			case "nil identity":
				envelope.Actions[0].ActionID = ProfileChoiceActionPrefix + "ipc_" + strings.Repeat("a", 26)
			}
			_, err := ProfileChoiceFromActions(envelope)
			require.Error(t, err)
		})
	}
}
