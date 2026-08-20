package httpapi

import (
	"crypto/sha256"
	"crypto/subtle"
	"net/http"
	"strings"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/httpjson"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/modelprovider"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage/modelstore"
	"github.com/omnara-ai/omnara/internal/storage/orglifecycle"
)

const (
	hostedCredentialCompletionPath         = "/internal/model-provider-credentials/complete"
	hostedCredentialCompletionMaxBodyBytes = 128 * 1024
)

type hostedCredentialCompletionRequest struct {
	OrgID           string `json:"org_id"`
	CreatorUserID   string `json:"creator_user_id"`
	Provisioner     string `json:"provisioner"`
	CredentialValue string `json:"credential_value"`
}

func (s *Server) hostedCredentialCompletionRoute(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	if !s.validHostedServiceAuthorization(r.Header.Get("Authorization")) {
		apierror.Write(w, openapi.ErrorCodeUnauthorized)
		return
	}
	if s.store == nil || s.defaultModelProvider == nil {
		apierror.Write(w, openapi.ErrorCodeServiceUnavailable)
		return
	}
	raw, ok := readIntegrationCallbackBody(w, r, hostedCredentialCompletionMaxBodyBytes)
	if !ok {
		return
	}
	var request hostedCredentialCompletionRequest
	if err := httpjson.DecodeStrictRequiredBytes(raw, &request); err != nil {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid completion payload")
		return
	}
	orgID, ok := canonicalHostedCompletionID(publicid.KindOrganization, request.OrgID)
	if !ok {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid organization id")
		return
	}
	creatorUserID, ok := canonicalHostedCompletionID(publicid.KindUser, request.CreatorUserID)
	if !ok {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid creator user id")
		return
	}
	if request.Provisioner == "" || request.Provisioner != strings.TrimSpace(request.Provisioner) {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid provisioner")
		return
	}
	if request.Provisioner != s.defaultModelProvider.Provisioner {
		apierror.Write(w, openapi.ErrorCodeStateTransitionConflict, "default provisioner changed")
		return
	}
	if err := modelprovider.ValidateHostedCredentialValue(request.CredentialValue); err != nil {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid credential value")
		return
	}

	created, err := s.store.Organizations().CompleteDefaultModelProviderProvisioning(
		r.Context(),
		orglifecycle.CompleteDefaultModelProviderProvisioningInput{
			OrgID:           orgID,
			CreatedByUserID: creatorUserID,
			Provider: modelstore.ProvisionedDefaultModelProvider{
				Template:        *s.defaultModelProvider,
				CredentialValue: request.CredentialValue,
			},
		},
	)
	if err != nil {
		s.log.Error(
			"complete hosted model provider credential",
			"org_id", request.OrgID,
			"provisioner", request.Provisioner,
			"error", err,
		)
		apierror.WriteError(w, apierror.FromError(err))
		return
	}
	s.log.Info(
		"hosted model provider credential completed",
		"org_id", request.OrgID,
		"provisioner", request.Provisioner,
		"created", created,
	)
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) validHostedServiceAuthorization(header string) bool {
	if s.hostedAPIToken == "" {
		return false
	}
	want := sha256.Sum256([]byte("Bearer " + s.hostedAPIToken))
	got := sha256.Sum256([]byte(header))
	return subtle.ConstantTimeCompare(got[:], want[:]) == 1
}

func canonicalHostedCompletionID(kind publicid.Kind, raw string) (orglifecycle.ID, bool) {
	id, err := publicid.Decode(kind, raw)
	if err != nil {
		return orglifecycle.NilID, false
	}
	canonical, err := publicid.Encode(kind, id)
	if err != nil || canonical != raw {
		return orglifecycle.NilID, false
	}
	return id, true
}
