package modelprovider

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"
	"unicode"

	"github.com/omnara-ai/omnara/internal/outboundhttp"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

const (
	HostedCredentialProvisionTimeout = 30 * time.Second
	HostedCredentialPath             = "/model-provider-credentials"
	MinimumHostedAPITokenBytes       = 32
	hostedCredentialMaxRequestBytes  = 256 * 1024
	hostedCredentialMaxResponseBytes = 64 * 1024
	hostedCredentialMaxSecretBytes   = 16 * 1024
)

type HostedCredentialProvisioner interface {
	ProvisionHostedCredential(
		context.Context,
		HostedCredentialRequest,
	) (ProvisionHostedCredentialResponse, error)
}

type HostedCredentialRequest struct {
	OrgID         string                                  `json:"org_id"`
	CreatorUserID string                                  `json:"creator_user_id"`
	Template      modelstore.DefaultModelProviderTemplate `json:"template"`
}

type ProvisionHostedCredentialResponse struct {
	CredentialValue string `json:"credential_value"`
	Pending         bool   `json:"-"`
}

var ErrHostedCredentialConflict = errors.New("hosted credential setup is blocked by an unresolved attempt")

type HTTPHostedCredentialProvisioner struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

func (p HTTPHostedCredentialProvisioner) ProvisionHostedCredential(
	ctx context.Context,
	request HostedCredentialRequest,
) (ProvisionHostedCredentialResponse, error) {
	if err := validateHostedCredentialRequest(request); err != nil {
		return ProvisionHostedCredentialResponse{}, err
	}
	response, err := p.doJSON(ctx, HostedCredentialPath, request)
	if err != nil {
		return ProvisionHostedCredentialResponse{}, err
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode == http.StatusAccepted {
		discardHostedErrorBody(response.Body)
		return ProvisionHostedCredentialResponse{Pending: true}, nil
	}
	if response.StatusCode != http.StatusCreated {
		discardHostedErrorBody(response.Body)
		if response.StatusCode == http.StatusConflict {
			return ProvisionHostedCredentialResponse{}, ErrHostedCredentialConflict
		}
		return ProvisionHostedCredentialResponse{}, fmt.Errorf("hosted API returned HTTP %d", response.StatusCode)
	}
	raw, err := readHostedJSONResponse(response)
	if err != nil {
		return ProvisionHostedCredentialResponse{}, err
	}
	var result ProvisionHostedCredentialResponse
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&result); err != nil {
		return ProvisionHostedCredentialResponse{}, fmt.Errorf("decode hosted credential response: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); !errors.Is(err, io.EOF) {
		return ProvisionHostedCredentialResponse{}, errors.New("decode hosted credential response: trailing JSON value")
	}
	if err := ValidateHostedCredentialValue(result.CredentialValue); err != nil {
		return ProvisionHostedCredentialResponse{}, err
	}
	return result, nil
}

func (p HTTPHostedCredentialProvisioner) doJSON(
	ctx context.Context,
	endpointPath string,
	request HostedCredentialRequest,
) (*http.Response, error) {
	endpoint, err := hostedCredentialEndpoint(p.BaseURL, endpointPath)
	if err != nil {
		return nil, err
	}
	if err := ValidateHostedAPIToken(p.Token); err != nil {
		return nil, err
	}
	body, err := encodeHostedCredentialRequest(request)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create hosted credential request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+p.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")

	client := p.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: HostedCredentialProvisionTimeout}
	}
	requestClient := *client
	if requestClient.Timeout == 0 || requestClient.Timeout > HostedCredentialProvisionTimeout {
		requestClient.Timeout = HostedCredentialProvisionTimeout
	}
	response, err := outboundhttp.CloneWithoutRedirects(&requestClient).Do(req)
	if err != nil {
		return nil, fmt.Errorf("call hosted API: %w", err)
	}
	return response, nil
}

func readHostedJSONResponse(response *http.Response) ([]byte, error) {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return nil, errors.New("hosted API response must be application/json")
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, hostedCredentialMaxResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read hosted credential response: %w", err)
	}
	if len(raw) > hostedCredentialMaxResponseBytes {
		return nil, errors.New("hosted credential response exceeds size limit")
	}
	return raw, nil
}

func discardHostedErrorBody(body io.Reader) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, hostedCredentialMaxResponseBytes+1))
}

func ValidateHostedAPIBaseURL(raw string) error {
	if raw == "" || raw != strings.TrimSpace(raw) {
		return errors.New("OMNARA_HOSTED_API_URL is required and cannot have surrounding whitespace")
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("parse OMNARA_HOSTED_API_URL: %w", err)
	}
	if parsed.Hostname() == "" || parsed.Opaque != "" {
		return errors.New("OMNARA_HOSTED_API_URL must be absolute and include a host")
	}
	if parsed.User != nil {
		return errors.New("OMNARA_HOSTED_API_URL must not include user information")
	}
	if parsed.Fragment != "" {
		return errors.New("OMNARA_HOSTED_API_URL must not include a fragment")
	}
	if !strings.EqualFold(parsed.Scheme, "http") && !strings.EqualFold(parsed.Scheme, "https") {
		return errors.New("OMNARA_HOSTED_API_URL must use HTTP or HTTPS")
	}
	if parsed.RawQuery != "" || parsed.ForceQuery {
		return errors.New("OMNARA_HOSTED_API_URL must contain only scheme, host, and an optional path")
	}
	return nil
}

func hostedCredentialEndpoint(baseURL, endpointPath string) (string, error) {
	if err := ValidateHostedAPIBaseURL(baseURL); err != nil {
		return "", err
	}
	parsed, err := url.Parse(baseURL)
	if err != nil {
		return "", fmt.Errorf("parse OMNARA_HOSTED_API_URL: %w", err)
	}
	parsed.Path = path.Join(parsed.Path, endpointPath)
	parsed.RawPath = ""
	return parsed.String(), nil
}

func ValidateHostedAPIToken(token string) error {
	if token == "" || token != strings.TrimSpace(token) {
		return errors.New("OMNARA_HOSTED_API_TOKEN is required and cannot have surrounding whitespace")
	}
	if len([]byte(token)) < MinimumHostedAPITokenBytes {
		return fmt.Errorf("OMNARA_HOSTED_API_TOKEN must contain at least %d bytes", MinimumHostedAPITokenBytes)
	}
	return nil
}

func ValidateHostedCredentialTemplateWireSize(template modelstore.DefaultModelProviderTemplate) error {
	request := HostedCredentialRequest{
		OrgID:         "org_agpwcurnyb3hhgzb2g3mtyfbw4",
		CreatorUserID: "usr_agpwcurnyb3hhgzb2g3mtyfbw4",
		Template:      template,
	}
	_, err := encodeHostedCredentialRequest(request)
	return err
}

func encodeHostedCredentialRequest(request HostedCredentialRequest) ([]byte, error) {
	var body bytes.Buffer
	if err := json.NewEncoder(&body).Encode(request); err != nil {
		return nil, fmt.Errorf("encode hosted credential request: %w", err)
	}
	if body.Len() > hostedCredentialMaxRequestBytes {
		return nil, errors.New("hosted credential request exceeds size limit")
	}
	return body.Bytes(), nil
}

func validateHostedCredentialRequest(request HostedCredentialRequest) error {
	if _, err := publicid.Decode(publicid.KindOrganization, request.OrgID); err != nil {
		return errors.New("org_id must be a valid public organization ID")
	}
	if _, err := publicid.Decode(publicid.KindUser, request.CreatorUserID); err != nil {
		return errors.New("creator_user_id must be a valid public user ID")
	}
	if request.Template.Provisioner == "" {
		return errors.New("template.provisioner is required")
	}
	return nil
}

// ValidateHostedCredentialValue validates a hosted credential for storage.
func ValidateHostedCredentialValue(value string) error {
	if value == "" || value != strings.TrimSpace(value) {
		return errors.New("credential_value is required and cannot have surrounding whitespace")
	}
	if len(value) > hostedCredentialMaxSecretBytes {
		return errors.New("credential_value exceeds size limit")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return errors.New("credential_value contains control characters")
		}
	}
	return nil
}
