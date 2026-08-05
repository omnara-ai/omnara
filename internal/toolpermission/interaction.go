package toolpermission

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"strings"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

const (
	AllowOptionIndex = 0
	DenyOptionIndex  = 1
)

type Authorization struct {
	ToolName string          `json:"tool_name"`
	Input    json.RawMessage `json:"input"`
}

type Request struct {
	Permission    Selection            `json:"permission"`
	Authorization Authorization        `json:"authorization"`
	Form          interactionform.Form `json:"form"`
}

type Decision string

const (
	DecisionAllow Decision = "allow"
	DecisionDeny  Decision = "deny"
)

type Resolution struct {
	Decision Decision
	Reason   string
}

type interactionMode struct {
	validate func(Request) error
	resolve  func(Request, interactionform.Resolution) (Resolution, error)
}

var interactionModes = map[string]interactionMode{
	ModeAlwaysAsk: {
		validate: validateAlwaysAskRequest,
		resolve:  resolveAlwaysAskRequest,
	},
}

func NewAuthorization(
	toolName string,
	input json.RawMessage,
) (Authorization, error) {
	toolName = strings.TrimSpace(toolName)
	if toolName == "" {
		return Authorization{}, errors.New("tool name is required")
	}
	if len(bytes.TrimSpace(input)) == 0 {
		input = json.RawMessage(`{}`)
	}
	canonicalInput, err := canonicalJSON(input)
	if err != nil {
		return Authorization{}, fmt.Errorf("canonicalize authorized input: %w", err)
	}
	return Authorization{
		ToolName: toolName,
		Input:    canonicalInput,
	}, nil
}

func NewRequest(
	descriptor ModeDescriptor,
	selection Selection,
	authorization Authorization,
	value interactionform.Form,
) (Request, error) {
	selection, err := ValidateSelection(selection, []ModeDescriptor{descriptor})
	if err != nil {
		return Request{}, err
	}
	request := Request{
		Permission:    selection,
		Authorization: authorization,
		Form:          value,
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

func ParseRequest(raw json.RawMessage) (Request, error) {
	var request Request
	if err := decodeStrictJSON(raw, &request); err != nil {
		return Request{}, fmt.Errorf("decode permission request: %w", err)
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

func (request *Request) Validate() error {
	authorization, err := NewAuthorization(
		request.Authorization.ToolName,
		request.Authorization.Input,
	)
	if err != nil {
		return err
	}
	request.Authorization = authorization
	request.Permission, err = NormalizeSelection(request.Permission)
	if err != nil {
		return err
	}
	mode, ok := interactionModes[request.Permission.Mode]
	if !ok {
		return fmt.Errorf(
			"permission mode %q does not support an interaction",
			request.Permission.Mode,
		)
	}
	if err := request.Form.Validate(); err != nil {
		return fmt.Errorf("permission interaction form: %w", err)
	}
	return mode.validate(*request)
}

func NewAllowDenyForm(
	title string,
	contextItems []interactionform.ContextItem,
) (interactionform.Form, error) {
	return interactionform.New(
		title,
		contextItems,
		[]interactionform.Question{
			{
				Prompt: "Allow this tool call?",
				Options: []interactionform.Option{
					{Label: "Allow"},
					{
						Label:      "Deny",
						AllowsText: true,
					},
				},
			},
		},
	)
}

func Resolve(
	request Request,
	response interactionform.Resolution,
) (Resolution, error) {
	if err := request.Validate(); err != nil {
		return Resolution{}, err
	}
	normalized, err := interactionform.NormalizeResolution(request.Form, response)
	if err != nil {
		return Resolution{}, err
	}
	mode := interactionModes[request.Permission.Mode]
	return mode.resolve(request, normalized)
}

func validateAlwaysAskRequest(request Request) error {
	if len(request.Form.Questions) != 1 {
		return errors.New("always_ask permission requires exactly one question")
	}
	question := request.Form.Questions[0]
	if question.Multiple || len(question.Options) != 2 {
		return errors.New("always_ask permission interaction form is invalid")
	}
	if question.Options[AllowOptionIndex].Label != "Allow" ||
		question.Options[AllowOptionIndex].AllowsText ||
		question.Options[DenyOptionIndex].Label != "Deny" ||
		!question.Options[DenyOptionIndex].AllowsText {
		return errors.New("always_ask permission interaction form is invalid")
	}
	return nil
}

func resolveAlwaysAskRequest(
	_ Request,
	response interactionform.Resolution,
) (Resolution, error) {
	if len(response.Answers) != 1 {
		return Resolution{}, errors.New("always_ask permission requires exactly one answer")
	}
	answer := response.Answers[0]
	if len(answer.OptionIndices) != 1 {
		return Resolution{}, errors.New("always_ask permission requires exactly one selected option")
	}
	switch answer.OptionIndices[0] {
	case AllowOptionIndex:
		return Resolution{Decision: DecisionAllow}, nil
	case DenyOptionIndex:
		reason := "tool call was denied"
		if answer.Text != "" {
			reason = answer.Text
		}
		return Resolution{Decision: DecisionDeny, Reason: reason}, nil
	default:
		return Resolution{}, fmt.Errorf(
			"always_ask permission does not support option %d",
			answer.OptionIndices[0],
		)
	}
}

func decodeStrictJSON(raw json.RawMessage, target any) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return errors.New("JSON value is required")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("trailing JSON value")
		}
		return err
	}
	return nil
}
