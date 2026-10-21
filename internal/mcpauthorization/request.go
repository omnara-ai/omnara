// Package mcpauthorization defines the request stored on an authorization
// interaction: an MCP tool call paused because the server asked for OAuth
// authorization the agent's credential does not carry.
package mcpauthorization

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"

	"github.com/google/uuid"

	"github.com/omnara-ai/omnara/internal/interactionform"
)

const (
	GrantOptionIndex   = 0
	DeclineOptionIndex = 1

	grantOptionLabel   = "Grant access"
	declineOptionLabel = "Decline"
)

type Request struct {
	ServerKey      string               `json:"server_key"`
	ToolName       string               `json:"tool_name"`
	MCPURL         string               `json:"mcp_url"`
	SecretID       *uuid.UUID           `json:"secret_id,omitempty"`
	SecretName     string               `json:"secret_name"`
	AgentConfigID  uuid.UUID            `json:"agent_config_id"`
	RequiredScopes []string             `json:"required_scopes"`
	GrantedScopes  []string             `json:"granted_scopes"`
	Form           interactionform.Form `json:"form"`
}

type NewRequestInput struct {
	ServerKey      string
	ToolName       string
	MCPURL         string
	SecretID       uuid.UUID
	SecretName     string
	AgentConfigID  uuid.UUID
	RequiredScopes []string
	GrantedScopes  []string
}

func NewRequest(input NewRequestInput) (Request, error) {
	request := Request{
		ServerKey:      input.ServerKey,
		ToolName:       input.ToolName,
		MCPURL:         input.MCPURL,
		SecretName:     input.SecretName,
		AgentConfigID:  input.AgentConfigID,
		RequiredScopes: input.RequiredScopes,
		GrantedScopes:  input.GrantedScopes,
	}
	if input.SecretID != uuid.Nil {
		secretID := input.SecretID
		request.SecretID = &secretID
	}
	request.normalize()
	form, err := newForm(request)
	if err != nil {
		return Request{}, err
	}
	request.Form = form
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

func ParseRequest(raw json.RawMessage) (Request, error) {
	var request Request
	if err := decodeStrictJSON(raw, &request); err != nil {
		return Request{}, fmt.Errorf("decode mcp authorization request: %w", err)
	}
	if err := request.Validate(); err != nil {
		return Request{}, err
	}
	return request, nil
}

func (request *Request) Validate() error {
	request.normalize()
	if request.ServerKey == "" || request.ToolName == "" || request.MCPURL == "" {
		return errors.New("mcp authorization request requires a server key, tool name, and MCP URL")
	}
	if request.SecretName == "" {
		return errors.New("mcp authorization request requires a secret name")
	}
	if request.SecretID != nil && *request.SecretID == uuid.Nil {
		return errors.New("mcp authorization request secret id must not be empty")
	}
	if request.AgentConfigID == uuid.Nil {
		return errors.New("mcp authorization request requires an agent config id")
	}
	if err := request.Form.Validate(); err != nil {
		return fmt.Errorf("mcp authorization interaction form: %w", err)
	}
	if len(request.Form.Questions) != 1 {
		return errors.New("mcp authorization interaction requires exactly one question")
	}
	question := request.Form.Questions[0]
	if question.Multiple || len(question.Options) != 2 ||
		question.Options[GrantOptionIndex].Label != grantOptionLabel ||
		question.Options[GrantOptionIndex].AllowsText ||
		question.Options[DeclineOptionIndex].Label != declineOptionLabel ||
		question.Options[DeclineOptionIndex].AllowsText {
		return errors.New("mcp authorization interaction form is invalid")
	}
	return nil
}

// RequestedScopes is what the authorization flow asks for: every scope the
// credential already holds plus the ones the server challenged for, so a
// provider that issues only the requested scopes does not drop earlier grants.
func (request *Request) RequestedScopes() []string {
	return UnionScopes(request.GrantedScopes, request.RequiredScopes)
}

// MissingScopes reports required scopes absent from granted.
func (request *Request) MissingScopes(granted []string) []string {
	var missing []string
	for _, scope := range request.RequiredScopes {
		if !slices.Contains(granted, scope) {
			missing = append(missing, scope)
		}
	}
	return missing
}

// Granted reports whether a resolution chose Grant access.
func Granted(request Request, resolution interactionform.Resolution) (bool, error) {
	normalized, err := interactionform.NormalizeResolution(request.Form, resolution)
	if err != nil {
		return false, err
	}
	switch normalized.Answers[0].OptionIndices[0] {
	case GrantOptionIndex:
		return true, nil
	case DeclineOptionIndex:
		return false, nil
	default:
		return false, fmt.Errorf(
			"mcp authorization does not support option %d",
			normalized.Answers[0].OptionIndices[0],
		)
	}
}

func GrantResolution() interactionform.Resolution {
	return interactionform.Resolution{
		Answers: []interactionform.Answer{{OptionIndices: []int{GrantOptionIndex}}},
	}
}

func UnionScopes(sets ...[]string) []string {
	var out []string
	for _, set := range sets {
		for _, scope := range set {
			scope = strings.TrimSpace(scope)
			if scope != "" && !slices.Contains(out, scope) {
				out = append(out, scope)
			}
		}
	}
	return out
}

func (request *Request) normalize() {
	request.ServerKey = strings.TrimSpace(request.ServerKey)
	request.ToolName = strings.TrimSpace(request.ToolName)
	request.MCPURL = strings.TrimSpace(request.MCPURL)
	request.SecretName = strings.TrimSpace(request.SecretName)
	request.RequiredScopes = UnionScopes(request.RequiredScopes)
	request.GrantedScopes = UnionScopes(request.GrantedScopes)
	if request.RequiredScopes == nil {
		request.RequiredScopes = []string{}
	}
	if request.GrantedScopes == nil {
		request.GrantedScopes = []string{}
	}
}

func newForm(request Request) (interactionform.Form, error) {
	contextItems := []interactionform.ContextItem{
		{Label: "MCP server", Value: request.ServerKey},
		{Label: "Tool", Value: request.ToolName},
	}
	if missing := request.MissingScopes(request.GrantedScopes); len(missing) > 0 {
		contextItems = append(contextItems, interactionform.ContextItem{
			Label: "Requested access",
			Value: strings.Join(missing, " "),
		})
	}
	prompt := fmt.Sprintf("Grant access to %s so the agent can call %s?", request.ServerKey, request.ToolName)
	if request.SecretID == nil {
		prompt = fmt.Sprintf("Connect %s so the agent can call %s?", request.ServerKey, request.ToolName)
	}
	return interactionform.New(
		fmt.Sprintf("%s needs authorization", request.ServerKey),
		contextItems,
		[]interactionform.Question{{
			Prompt: prompt,
			Options: []interactionform.Option{
				{Label: grantOptionLabel},
				{Label: declineOptionLabel},
			},
		}},
	)
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
