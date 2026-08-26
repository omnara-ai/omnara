package auth

import (
	"errors"
	"net/http"
	"net/url"

	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/resourcename"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	devicePollLimit       = 240
	devicePollClientLimit = 600
)

func (h *Handler) startDeviceAuthRoute(w http.ResponseWriter, r *http.Request) {
	form, ok := parseOAuthForm(w, r)
	if !ok {
		return
	}
	clientID := oauthFormValue(form, "client_id")
	if clientID == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "client_id is required")
		return
	}
	client, ok := oauthPublicClients[clientID]
	if !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "client_id is not registered")
		return
	}
	if oauthFormValue(form, "scope") != "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_scope", "this authorization server does not define OAuth scopes")
		return
	}
	tokenName, err := resourcename.CanonicalizeAllowEmpty("token_name", oauthFormValue(form, "token_name"))
	if err != nil {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	if !h.requireOAuthRateLimits(
		w,
		r,
		"device_code",
		h.clientBucket(r),
		ResetClientLimit,
		ResetClientLimit,
		ResetClientLimit,
		authShortWindow,
		false,
	) {
		return
	}
	flow, err := h.store.StartDeviceAuthFlow(
		r.Context(),
		identitystore.StartDeviceAuthFlowInput{
			ClientID:   clientID,
			ClientName: client.name,
			TokenName:  tokenName,
		},
	)
	if err != nil {
		if errors.Is(err, storeerr.ErrInvalidDeviceAuthFlow) {
			writeOAuthError(w, http.StatusBadRequest, "invalid_request", "device authorization request is invalid")
			return
		}
		h.writeOAuthServerError(w, r, err)
		return
	}
	verificationURI := h.authURL("/device", nil)
	values := url.Values{"user_code": []string{flow.UserCode}}
	writeOAuthJSON(w, http.StatusOK, map[string]any{
		"device_code":               flow.DeviceCode,
		"user_code":                 flow.UserCode,
		"verification_uri":          verificationURI,
		"verification_uri_complete": h.authURL("/device", values),
		"expires_in":                int(flow.ExpiresIn.Seconds()),
		"interval":                  int(flow.Interval.Seconds()),
	})
}

func (h *Handler) pollDeviceAuthRoute(w http.ResponseWriter, r *http.Request) {
	form, ok := parseOAuthForm(w, r)
	if !ok {
		return
	}
	grantType := oauthFormValue(form, "grant_type")
	if grantType == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "grant_type is required")
		return
	}
	if grantType != OAuthDeviceGrantType {
		writeOAuthError(w, http.StatusBadRequest, "unsupported_grant_type", "grant_type is not supported")
		return
	}
	deviceCode := oauthFormValue(form, "device_code")
	clientID := oauthFormValue(form, "client_id")
	if deviceCode == "" || clientID == "" {
		writeOAuthError(w, http.StatusBadRequest, "invalid_request", "device_code and client_id are required")
		return
	}
	if _, ok := oauthPublicClients[clientID]; !ok {
		writeOAuthError(w, http.StatusBadRequest, "invalid_client", "client_id is not registered")
		return
	}
	if !h.requireOAuthRateLimits(
		w,
		r,
		"device_token",
		deviceCode,
		devicePollLimit,
		devicePollLimit,
		devicePollClientLimit,
		authShortWindow,
		true,
	) {
		return
	}
	result, err := h.store.PollDeviceAuthFlow(
		r.Context(),
		identitystore.DeviceAuthFlowPollInput{DeviceCode: deviceCode, ClientID: clientID},
	)
	if err != nil {
		h.writeOAuthTokenStorageError(w, r, err)
		return
	}
	if result.Status != identitystore.DeviceAuthFlowStatusApproved {
		writeOAuthError(w, http.StatusBadRequest, string(result.Status), "")
		return
	}
	writeOAuthJSON(w, http.StatusOK, map[string]any{"access_token": result.Token, "token_type": "Bearer"})
}

func (h *Handler) writeOAuthTokenStorageError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, storeerr.ErrUnauthorized) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "device authorization grant is invalid")
		return
	}
	if errors.Is(err, storeerr.ErrConflict) {
		writeOAuthError(w, http.StatusBadRequest, "invalid_grant", "personal access token limit reached")
		return
	}
	h.writeOAuthServerError(w, r, err)
}

func (h *Handler) pendingDeviceAuthRoute(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.currentPrincipal(r.Context())
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.ID == storage.NilID ||
		principal.BrowserSessionID == storage.NilID {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	userCode := r.URL.Query().Get("user_code")
	if !h.requireDeviceUserCodeRateLimits(w, r, "device_pending", principal.ID, userCode) {
		return
	}
	flow, err := h.store.PendingDeviceAuthFlow(
		r.Context(),
		identitystore.DeviceAuthFlowPendingInput{UserCode: userCode},
	)
	if err != nil {
		h.writeDeviceApprovalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"client_name": flow.ClientName,
		"token_name":  flow.TokenName,
		"created_at":  flow.CreatedAt,
		"expires_at":  flow.ExpiresAt,
	})
}

func (h *Handler) approveDeviceAuthRoute(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.currentPrincipal(r.Context())
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.ID == storage.NilID ||
		principal.BrowserSessionID == storage.NilID {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var body struct {
		UserCode string `json:"user_code"`
	}
	if err := decodeAllowedJSONBody(r, &body, map[string]bool{"user_code": true}, nil); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	if !h.requireDeviceUserCodeRateLimits(w, r, "device_approve", principal.ID, body.UserCode) {
		return
	}
	if err := h.store.ApproveDeviceAuthFlow(
		r.Context(),
		identitystore.ApproveDeviceAuthFlowInput{
			UserCode:                 body.UserCode,
			UserID:                   principal.ID,
			ApprovedBrowserSessionID: principal.BrowserSessionID,
		},
	); err != nil {
		h.writeDeviceApprovalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) denyDeviceAuthRoute(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.currentPrincipal(r.Context())
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.ID == storage.NilID ||
		principal.BrowserSessionID == storage.NilID {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	if !requireJSONContentType(w, r) {
		return
	}
	var body struct {
		UserCode string `json:"user_code"`
	}
	if err := decodeAllowedJSONBody(r, &body, map[string]bool{"user_code": true}, nil); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	if !h.requireDeviceUserCodeRateLimits(w, r, "device_deny", principal.ID, body.UserCode) {
		return
	}
	if err := h.store.DenyDeviceAuthFlow(
		r.Context(),
		identitystore.DenyDeviceAuthFlowInput{UserCode: body.UserCode},
	); err != nil {
		h.writeDeviceApprovalError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) requireDeviceUserCodeRateLimits(
	w http.ResponseWriter,
	r *http.Request,
	action string,
	userID storage.ID,
	userCode string,
) bool {
	code := identitystore.NormalizeDeviceUserCode(userCode)
	if !h.requireAuthRateLimits(
		w,
		r,
		action+"_code",
		code,
		TokenConsumeLimit,
		TokenConsumeLimit,
		TokenClientLimit,
		authShortWindow,
	) {
		return false
	}
	return h.requireAuthRateLimits(
		w,
		r,
		action+"_principal",
		userID.String(),
		LoginLimit,
		LoginSubjectLimit,
		TokenClientLimit,
		authShortWindow,
	)
}

func (h *Handler) writeDeviceApprovalError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, storeerr.ErrUnauthorized) {
		apierror.Write(w, openapi.ErrorCodeInvalidRequest, "invalid or expired device code")
		return
	}
	h.writeAuthServerError(w, r, err)
}
