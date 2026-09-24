package discord

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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

func profileChoiceTestOptions() []ProfileChoiceOption {
	return []ProfileChoiceOption{{Key: "one", Name: "First"}, {Key: "two", Name: "Second"}}
}

func TestProfileChoicePromptNativeSelectRoundTrip(t *testing.T) {
	t.Parallel()
	for _, count := range []int{2, 16} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			t.Parallel()
			choiceID := profileChoiceTestID(t)
			options := make([]ProfileChoiceOption, count)
			for i := range options {
				options[i] = ProfileChoiceOption{Key: fmt.Sprintf("slot-%d", i), Name: strings.Repeat("🦉", 120)}
			}
			text, rows, err := ProfileChoicePrompt(choiceID, options, "Available until 12:00 UTC.")
			require.NoError(t, err)
			require.Contains(t, text, "Choose a profile before continuing")
			require.Contains(t, text, "original request")
			require.Contains(t, text, "Anyone with access to this conversation can choose")
			require.Contains(t, text, "Available until 12:00 UTC.")
			require.NoError(t, validateRows(rows, false))
			require.Len(t, rows, 1)
			require.Equal(t, 1, rows[0].Type)
			require.Len(t, rows[0].Components, 1)
			menu := rows[0].Components[0]
			require.Equal(t, 3, menu.Type)
			require.Equal(t, 1, menu.MinValues)
			require.Equal(t, 1, menu.MaxValues)
			require.Equal(t, ProfileChoiceCustomIDPrefix+choiceID, menu.CustomID)
			require.LessOrEqual(t, len(menu.CustomID), 100)
			_, err = DecodeCustomID(menu.CustomID)
			require.Error(t, err, "profile choices cannot resolve an agent interaction")
			require.Len(t, menu.Options, count)
			for i, option := range menu.Options {
				require.True(t, utf8.ValidString(option.Label))
				require.Equal(t, 100, utf8.RuneCountInString(option.Label))
				require.Equal(t, options[i].Key, option.Value)
			}
			raw, err := json.Marshal(menu)
			require.NoError(t, err)
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &fields))
			require.NotContains(t, fields, "style")
			require.NotContains(t, fields, "label")
			require.NotContains(t, fields, "required")
			var callback Interaction
			require.NoError(t, json.Unmarshal([]byte(fmt.Sprintf(
				`{"type":3,"data":{"component_type":3,"custom_id":%q,"values":[%q]}}`,
				menu.CustomID, options[count-1].Key)), &callback))
			selection, err := ProfileChoiceFromInteraction(callback)
			require.NoError(t, err)
			require.Equal(t, ProfileChoiceSelection{ChoiceID: choiceID, Key: options[count-1].Key}, selection)
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
			options := profileChoiceTestOptions()
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
	require.LessOrEqual(t, utf8.RuneCountInString(text), 2000)
}

func TestProfileChoiceRowsPreserveButtonAndModalValidation(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"arbitrary callback", "agent callback", "button style", "label", "required", "text length",
		"min zero", "max multiple", "long placeholder", "long label", "duplicate options", "too many options",
		"mixed row", "duplicate rows", "modal", "button options", "modal options",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			_, rows, err := ProfileChoicePrompt(profileChoiceTestID(t), profileChoiceTestOptions(), "")
			require.NoError(t, err)
			menu := &rows[0].Components[0]
			modal := false
			switch scenario {
			case "arbitrary callback":
				menu.CustomID = "customer:launch:profile"
			case "agent callback":
				menu.CustomID = callbackID(t)
			case "button style":
				menu.Style = 1
			case "label":
				menu.Label = "Unexpected button label"
			case "required":
				value := true
				menu.Required = &value
			case "text length":
				menu.MaxLength = 4000
			case "min zero":
				menu.MinValues = 0
			case "max multiple":
				menu.MaxValues = 2
			case "long placeholder":
				menu.Placeholder = strings.Repeat("x", 151)
			case "long label":
				menu.Options[0].Label = strings.Repeat("x", 101)
			case "duplicate options":
				menu.Options[1].Value = menu.Options[0].Value
			case "too many options":
				menu.Options = make([]SelectOption, 17)
			case "mixed row":
				rows[0].Components = append(rows[0].Components,
					Component{Type: 2, Style: 1, Label: "Answer", CustomID: callbackID(t)})
			case "duplicate rows":
				rows = append(rows, rows[0])
			case "modal":
				modal = true
			case "button options":
				*menu = Component{Type: 2, Style: 1, Label: "Answer", CustomID: callbackID(t), Options: menu.Options}
			case "modal options":
				modal = true
				*menu = Component{Type: 4, Style: 2, Label: "Answer", CustomID: "q0", MaxLength: 4000, Options: menu.Options}
			}
			require.Error(t, validateRows(rows, modal))
		})
	}
}

func TestProfileChoiceSignedIntakeAcceptsOnlySingleNativeSelection(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"valid", "missing values", "multiple values", "duplicate values", "empty value", "padded value", "long value",
		"wrong namespace", "wrong identity", "extra authority", "nil identity", "button", "modal", "missing component type",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			choiceID := profileChoiceTestID(t)
			input := Interaction{ID: "999", ApplicationID: "111", Type: 3, ChannelID: "555", User: &User{ID: "777"},
				Data: InteractionData{CustomID: ProfileChoiceCustomIDPrefix + choiceID, ComponentType: 3, Values: []string{"one"}}}
			switch scenario {
			case "missing values":
				input.Data.Values = nil
			case "multiple values":
				input.Data.Values = []string{"one", "two"}
			case "duplicate values":
				input.Data.Values = []string{"one", "one"}
			case "empty value":
				input.Data.Values = []string{""}
			case "padded value":
				input.Data.Values = []string{" one"}
			case "long value":
				input.Data.Values = []string{strings.Repeat("x", 65)}
			case "wrong namespace":
				input.Data.CustomID = "launch:" + choiceID
			case "wrong identity":
				input.Data.CustomID = ProfileChoiceCustomIDPrefix + "not-a-choice"
			case "extra authority":
				input.Data.CustomID += ":profile-id"
			case "nil identity":
				input.Data.CustomID = ProfileChoiceCustomIDPrefix + "ipc_" + strings.Repeat("a", 26)
			case "button":
				input.Data.ComponentType = 2
			case "modal":
				input.Type = 5
			case "missing component type":
				input.Data.ComponentType = 0
			}
			calls := 0
			intake := func(_ context.Context, interaction Interaction) (InteractionResponse, error) {
				calls++
				selection, err := ProfileChoiceFromInteraction(interaction)
				require.NoError(t, err)
				require.Equal(t, ProfileChoiceSelection{ChoiceID: choiceID, Key: "one"}, selection)
				return Acknowledge(interaction), nil
			}
			handler, private := interactionFixture(t, intake)
			raw, err := json.Marshal(input)
			require.NoError(t, err)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, signedRequest(private, string(raw), time.Now()))
			if scenario == "valid" {
				require.Equal(t, http.StatusOK, w.Code)
				require.JSONEq(t, `{"type":6}`, w.Body.String())
				require.Equal(t, 1, calls)
			} else {
				require.Equal(t, http.StatusBadRequest, w.Code)
				require.Zero(t, calls)
				_, err := ProfileChoiceFromInteraction(input)
				require.Error(t, err)
			}
		})
	}
}

func TestProfileChoiceCreateMessageUsesNativeSelector(t *testing.T) {
	t.Parallel()
	text, rows, err := ProfileChoicePrompt(profileChoiceTestID(t), profileChoiceTestOptions(), "")
	require.NoError(t, err)
	posts := 0
	client, _ := testClient(t, prepared(func(w http.ResponseWriter, r *http.Request) {
		posts++
		if r.Method != http.MethodPost || r.URL.Path != "/api/v10/channels/555/messages" {
			t.Error("unexpected menu endpoint")
		}
		var payload messagePayload
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if payload.Content != text || len(payload.Components) != 1 || len(payload.AllowedMentions.Parse) != 0 ||
			validateRows(payload.Components, false) != nil {
			t.Error("invalid native menu payload")
		}
		fmt.Fprint(w, messageJSON)
	}))
	_, err = client.CreateMessage(t.Context(), scope(), MessageArgs{Content: text, Components: rows, Nonce: "send_1"})
	require.NoError(t, err)
	require.Equal(t, 1, posts)
}

func TestProfileChoiceSignedResponseUpdatesMessageAndClearsMenu(t *testing.T) {
	t.Parallel()
	for _, clearMenu := range []bool{true, false} {
		t.Run(fmt.Sprint(clearMenu), func(t *testing.T) {
			t.Parallel()
			choiceID := profileChoiceTestID(t)
			calls := 0
			intake := func(_ context.Context, input Interaction) (InteractionResponse, error) {
				calls++
				selection, err := ProfileChoiceFromInteraction(input)
				require.NoError(t, err)
				require.Equal(t, ProfileChoiceSelection{ChoiceID: choiceID, Key: "one"}, selection)
				data := &InteractionResponseData{Content: "Profile selected."}
				if clearMenu {
					data.Components = []ActionRow{}
				}
				return InteractionResponse{Type: 7, Data: data}, nil
			}
			handler, private := interactionFixture(t, intake)
			body := fmt.Sprintf(`{"id":"999","application_id":"111","type":3,"channel_id":"555",
"user":{"id":"777"},"data":{"component_type":3,"custom_id":%q,"values":["one"]}}`,
				ProfileChoiceCustomIDPrefix+choiceID)
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, signedRequest(private, body, time.Now()))
			require.Equal(t, http.StatusOK, w.Code)
			require.Equal(t, 1, calls)
			want := `{"type":7,"data":{"content":"Profile selected.","allowed_mentions":{"parse":[]}}}`
			if clearMenu {
				want = `{"type":7,"data":{"content":"Profile selected.","components":[],"allowed_mentions":{"parse":[]}}}`
			}
			require.JSONEq(t, want, w.Body.String())
		})
	}
}

func TestProfileChoiceUpdateResponseValidation(t *testing.T) {
	t.Parallel()
	for _, scenario := range []string{
		"valid", "slash command", "modal submission", "ephemeral", "other flags", "blank", "missing data",
		"long content", "invalid utf8", "title", "custom ID", "invalid rows", "arbitrary callback",
	} {
		t.Run(scenario, func(t *testing.T) {
			t.Parallel()
			inputType := 3
			response := InteractionResponse{Type: 7, Data: &InteractionResponseData{
				Content: strings.Repeat("界", 2000), Components: []ActionRow{},
			}}
			switch scenario {
			case "slash command":
				inputType = 2
			case "modal submission":
				inputType = 5
			case "ephemeral":
				response.Data.Flags = 64
			case "other flags":
				response.Data.Flags = 4
			case "blank":
				response.Data.Content = ""
			case "missing data":
				response.Data = nil
			case "long content":
				response.Data.Content += "界"
			case "invalid utf8":
				response.Data.Content = string([]byte{255})
			case "title":
				response.Data.Title = "Modal"
			case "custom ID":
				response.Data.CustomID = callbackID(t)
			case "invalid rows":
				response.Data.Components = []ActionRow{{Type: 1}}
			case "arbitrary callback":
				response.Data.Components = []ActionRow{{Type: 1, Components: []Component{
					{Type: 2, Style: 1, Label: "Answer", CustomID: "unvalidated-callback"},
				}}}
			}
			require.Equal(t, scenario == "valid", validResponse(inputType, response))
		})
	}
}
