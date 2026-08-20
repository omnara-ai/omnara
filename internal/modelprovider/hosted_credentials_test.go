package modelprovider

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/modelprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
)

const testHostedAPIToken = "0123456789abcdef0123456789abcdef"

func TestHTTPHostedCredentialProvisionerBuildsAuthenticatedRoute(t *testing.T) {
	t.Parallel()
	var gotRequest HostedCredentialRequest
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/private/model-provider-credentials" {
			t.Errorf("request = %s %s, want hosted credential route", r.Method, r.URL.Path)
		}
		if got := r.Header.Get("Authorization"); got != "Bearer "+testHostedAPIToken {
			t.Errorf("authorization = %q, want bearer service token", got)
		}
		if got := r.Header.Get("Content-Type"); got != "application/json" {
			t.Errorf("content type = %q, want application/json", got)
		}
		if err := json.NewDecoder(r.Body).Decode(&gotRequest); err != nil {
			t.Errorf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"credential_value":"sk-provisioned"}`))
	}))
	defer server.Close()

	provisioner := HTTPHostedCredentialProvisioner{
		BaseURL:    server.URL + "/private/",
		Token:      testHostedAPIToken,
		HTTPClient: server.Client(),
	}
	response, err := provisioner.ProvisionHostedCredential(context.Background(), validHostedCredentialRequest())
	if err != nil {
		t.Fatalf("provision hosted credential: %v", err)
	}
	if response.CredentialValue != "sk-provisioned" {
		t.Fatalf("credential value = %q, want provisioned value", response.CredentialValue)
	}
	if !reflect.DeepEqual(gotRequest, validHostedCredentialRequest()) {
		t.Fatalf("unexpected provision request: %+v", gotRequest)
	}
}

func TestHTTPHostedCredentialProvisionerAcceptsAdditiveResponseFields(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"credential_value":"secret","private_policy":{"version":2}}`))
	}))
	defer server.Close()

	provisioner := HTTPHostedCredentialProvisioner{
		BaseURL:    server.URL,
		Token:      testHostedAPIToken,
		HTTPClient: server.Client(),
	}
	response, err := provisioner.ProvisionHostedCredential(context.Background(), validHostedCredentialRequest())
	if err != nil {
		t.Fatalf("provision credential: %v", err)
	}
	if response.CredentialValue != "secret" {
		t.Fatalf("credential value = %q, want secret", response.CredentialValue)
	}
}

func TestHTTPHostedCredentialProvisionerAcceptsDurablyPendingRequest(t *testing.T) {
	t.Parallel()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"status":"pending","private_state":"not-for-the-control-plane"}`))
	}))
	defer server.Close()

	provisioner := HTTPHostedCredentialProvisioner{
		BaseURL:    server.URL,
		Token:      testHostedAPIToken,
		HTTPClient: server.Client(),
	}
	response, err := provisioner.ProvisionHostedCredential(context.Background(), validHostedCredentialRequest())
	if err != nil {
		t.Fatalf("accept pending credential: %v", err)
	}
	if !response.Pending || response.CredentialValue != "" {
		t.Fatalf("pending response = %+v, want pending without credential", response)
	}
}

func TestHTTPHostedCredentialProvisionerClassifiesOnlyConflictStatus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		status       int
		contentType  string
		body         string
		wantConflict bool
	}{
		{
			name:         "opaque conflict",
			status:       http.StatusConflict,
			contentType:  "text/plain",
			body:         "private issuance state and sensitive-upstream-detail",
			wantConflict: true,
		},
		{
			name:         "oversized opaque conflict",
			status:       http.StatusConflict,
			contentType:  "application/octet-stream",
			body:         strings.Repeat("x", hostedCredentialMaxResponseBytes+1024),
			wantConflict: true,
		},
		{
			name:        "bad request",
			status:      http.StatusBadRequest,
			contentType: "application/json",
			body:        `{"error":{"code":"private_invalid_request"}}`,
		},
		{
			name:        "upstream unavailable",
			status:      http.StatusServiceUnavailable,
			contentType: "application/json",
			body:        `{"error":{"code":"private_upstream_state"}}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			provisioner := HTTPHostedCredentialProvisioner{
				BaseURL: server.URL, Token: testHostedAPIToken, HTTPClient: server.Client(),
			}
			_, err := provisioner.ProvisionHostedCredential(context.Background(), validHostedCredentialRequest())
			if errors.Is(err, ErrHostedCredentialConflict) != test.wantConflict {
				t.Fatalf("error = %v, conflict=%v want %v", err, errors.Is(err, ErrHostedCredentialConflict), test.wantConflict)
			}
			if err == nil || strings.Contains(err.Error(), test.body) ||
				strings.Contains(err.Error(), "sensitive-upstream-detail") {
				t.Fatalf("failure exposes hosted error detail: %v", err)
			}
		})
	}
}

func TestHTTPHostedCredentialProvisionerRejectsUnsafeResponses(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		statusCode  int
		contentType string
		body        string
		wantError   string
	}{
		{
			name:        "service failure",
			statusCode:  http.StatusBadGateway,
			contentType: "application/json",
			body:        `{"error":"secret"}`,
			wantError:   "HTTP 502",
		},
		{
			name:        "ok is not replayable credential creation",
			statusCode:  http.StatusOK,
			contentType: "application/json",
			body:        `{"credential_value":"secret"}`,
			wantError:   "HTTP 200",
		},
		{
			name:        "wrong content type",
			statusCode:  http.StatusCreated,
			contentType: "text/plain",
			body:        `{"credential_value":"secret"}`,
			wantError:   "application/json",
		},
		{
			name:        "empty credential",
			statusCode:  http.StatusCreated,
			contentType: "application/json",
			body:        `{"credential_value":""}`,
			wantError:   "credential_value",
		},
		{
			name:        "trailing response",
			statusCode:  http.StatusCreated,
			contentType: "application/json",
			body:        `{"credential_value":"secret"}{}`,
			wantError:   "trailing JSON",
		},
		{
			name:        "oversized response",
			statusCode:  http.StatusCreated,
			contentType: "application/json",
			body:        `{"credential_value":"` + strings.Repeat("x", hostedCredentialMaxResponseBytes) + `"}`,
			wantError:   "size limit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", test.contentType)
				w.WriteHeader(test.statusCode)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			provisioner := HTTPHostedCredentialProvisioner{
				BaseURL:    server.URL,
				Token:      testHostedAPIToken,
				HTTPClient: server.Client(),
			}
			_, err := provisioner.ProvisionHostedCredential(context.Background(), validHostedCredentialRequest())
			if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want containing %q", err, test.wantError)
			}
			if strings.Contains(err.Error(), test.body) {
				t.Fatalf("error %q exposes hosted response body", err)
			}
		})
	}
}

func TestHTTPHostedCredentialProvisionerRejectsRedirect(t *testing.T) {
	t.Parallel()
	destinationCalled := make(chan struct{}, 1)
	destination := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		destinationCalled <- struct{}{}
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusTemporaryRedirect)
	}))
	defer redirect.Close()

	provisioner := HTTPHostedCredentialProvisioner{
		BaseURL:    redirect.URL,
		Token:      testHostedAPIToken,
		HTTPClient: redirect.Client(),
	}
	_, err := provisioner.ProvisionHostedCredential(context.Background(), validHostedCredentialRequest())
	if err == nil || !strings.Contains(err.Error(), "HTTP 307") {
		t.Fatalf("error = %v, want HTTP 307", err)
	}
	select {
	case <-destinationCalled:
		t.Fatal("hosted client followed redirect")
	default:
	}
}

func TestHostedAPIConfigurationValidation(t *testing.T) {
	t.Parallel()
	if err := ValidateHostedAPIBaseURL("http://hosted.example.test/internal"); err != nil {
		t.Fatalf("HTTP hosted URL: %v", err)
	}
	if err := ValidateHostedAPIBaseURL("http://127.0.0.1:4310/internal"); err != nil {
		t.Fatalf("loopback hosted URL: %v", err)
	}
	endpoint, err := hostedCredentialEndpoint(
		"http://saas-api:4301/internal/",
		HostedCredentialPath,
	)
	if err != nil || endpoint != "http://saas-api:4301/internal/model-provider-credentials" {
		t.Fatalf("endpoint = %q err=%v", endpoint, err)
	}
	if err := ValidateHostedAPIToken(strings.Repeat("a", MinimumHostedAPITokenBytes-1)); err == nil {
		t.Fatal("short hosted API token accepted")
	}
	if err := ValidateHostedAPIToken(testHostedAPIToken); err != nil {
		t.Fatalf("valid hosted API token: %v", err)
	}
}

func TestValidateHostedCredentialTemplateWireSize(t *testing.T) {
	template := validHostedCredentialTemplate()
	if err := ValidateHostedCredentialTemplateWireSize(template); err != nil {
		t.Fatalf("normal hosted credential template: %v", err)
	}
	template.Models[0].ProviderModelSlug = strings.Repeat("x", hostedCredentialMaxRequestBytes)
	if err := ValidateHostedCredentialTemplateWireSize(template); err == nil ||
		!strings.Contains(err.Error(), "request exceeds size limit") {
		t.Fatalf("oversized template error = %v, want request size limit", err)
	}
}

func validHostedCredentialRequest() HostedCredentialRequest {
	creatorUserID, err := publicid.Encode(
		publicid.KindUser,
		uuid.MustParse("01922e74-9d00-7000-8000-000000000001"),
	)
	if err != nil {
		panic(err)
	}
	return HostedCredentialRequest{
		OrgID:         "org_agpwcurnyb3hhgzb2g3mtyfbw4",
		CreatorUserID: creatorUserID,
		Template:      validHostedCredentialTemplate(),
	}
}

func validHostedCredentialTemplate() modelstore.DefaultModelProviderTemplate {
	template, err := modelstore.PrepareDefaultModelProviderTemplate(modelstore.DefaultModelProviderTemplate{
		Provisioner:          "openrouter",
		Name:                 "hosted-provider",
		CredentialSecretName: "hosted-provider-key",
		APIFormat:            modelprotocol.APIFormatOpenAIChatCompletions,
		APIVariant:           modelprotocol.APIVariantOpenRouter,
		BaseURL:              "https://openrouter.ai/api/v1",
		Models: []modelstore.DefaultConfiguredModelTemplate{{
			Name:                "model",
			ProviderModelSlug:   "vendor/model",
			ContextWindowTokens: 128000,
			MaxOutputTokens:     8192,
		}},
	})
	if err != nil {
		panic(err)
	}
	return template
}
