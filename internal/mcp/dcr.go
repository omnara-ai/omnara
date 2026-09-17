package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/modelcontextprotocol/go-sdk/oauthex"
	"github.com/omnara-ai/omnara/internal/urlpolicy"
)

type ClientRegistration struct {
	ClientID     string
	ClientSecret string
}

type ClientRegistrationError struct {
	Code        string
	Description string
}

func (e *ClientRegistrationError) Error() string {
	if e.Description == "" {
		return "registration rejected: " + e.Code
	}
	return "registration rejected: " + e.Code + " (" + e.Description + ")"
}

func RegisterClient(
	ctx context.Context,
	registrationEndpoint string,
	metadata *oauthex.ClientRegistrationMetadata,
	client *http.Client,
) (ClientRegistration, error) {
	if err := urlpolicy.RequireHTTPSOrLoopback(registrationEndpoint); err != nil {
		return ClientRegistration{}, fmt.Errorf("registration_endpoint: %w", err)
	}
	payload, err := json.Marshal(metadata)
	if err != nil {
		return ClientRegistration{}, fmt.Errorf("encode client metadata: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, registrationEndpoint, bytes.NewReader(payload))
	if err != nil {
		return ClientRegistration{}, fmt.Errorf("build registration request: %w", err)
	}
	request.Header.Set("Content-Type", mediaTypeJSON)
	request.Header.Set("Accept", mediaTypeJSON)
	response, err := clientWithoutRedirects(client).Do(request)
	if err != nil {
		return ClientRegistration{}, fmt.Errorf("registration request: %w", err)
	}
	defer response.Body.Close() //nolint:errcheck // Response body close errors are not actionable here.
	body, err := readOAuthResponseBody(response.Body)
	if err != nil {
		return ClientRegistration{}, fmt.Errorf("read registration response: %w", err)
	}
	switch {
	case response.StatusCode == http.StatusOK || response.StatusCode == http.StatusCreated:
		var registered struct {
			ClientID     string `json:"client_id"`
			ClientSecret string `json:"client_secret"`
		}
		if err := json.Unmarshal(body, &registered); err != nil {
			return ClientRegistration{}, fmt.Errorf("parse registration response: %w", err)
		}
		if registered.ClientID == "" {
			return ClientRegistration{}, errors.New("registration response is missing client_id")
		}
		return ClientRegistration{ClientID: registered.ClientID, ClientSecret: registered.ClientSecret}, nil
	case response.StatusCode == http.StatusBadRequest:
		var rejected struct {
			Code        string `json:"error"`
			Description string `json:"error_description"`
		}
		if err := json.Unmarshal(body, &rejected); err != nil || rejected.Code == "" {
			return ClientRegistration{}, &HTTPError{Status: response.StatusCode, Body: bytes.TrimSpace(body)}
		}
		return ClientRegistration{}, &ClientRegistrationError{Code: rejected.Code, Description: rejected.Description}
	default:
		preview := body
		if len(preview) > statusErrorDecodeBytes {
			preview = preview[:statusErrorDecodeBytes]
		}
		return ClientRegistration{}, &HTTPError{Status: response.StatusCode, Body: bytes.TrimSpace(preview)}
	}
}
