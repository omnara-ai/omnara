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
	if !requireJSONContentType(w, r) {
		return
	}
	var body struct {
		ClientName string `json:"client_name"`
		TokenName  string `json:"token_name"`
	}
	if err := decodeAllowedJSONBody(r, &body, map[string]bool{"client_name": true, "token_name": true}, nil); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	clientName, err := resourcename.CanonicalizeAllowEmpty("client_name", body.ClientName)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	body.ClientName = clientName
	tokenName, err := resourcename.CanonicalizeAllowEmpty("token_name", body.TokenName)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	body.TokenName = tokenName
	if !h.requireAuthRateLimits(
		w,
		r,
		"device_code",
		h.clientBucket(r),
		ResetClientLimit,
		ResetClientLimit,
		ResetClientLimit,
		authShortWindow,
	) {
		return
	}
	flow, err := h.store.StartDeviceAuthFlow(
		r.Context(),
		identitystore.StartDeviceAuthFlowInput{ClientName: body.ClientName, TokenName: body.TokenName},
	)
	if err != nil {
		if errors.Is(err, storeerr.ErrInvalidDeviceAuthFlow) {
			apierror.WriteError(w, apierror.FromError(err))
			return
		}
		h.writeAuthServerError(w, r, err)
		return
	}
	verificationURI := h.authURL("/device", nil)
	values := url.Values{"user_code": []string{flow.UserCode}}
	writeJSON(w, http.StatusOK, map[string]any{
		"device_code":               flow.DeviceCode,
		"user_code":                 flow.UserCode,
		"verification_uri":          verificationURI,
		"verification_uri_complete": h.authURL("/device", values),
		"expires_in":                int(flow.ExpiresIn.Seconds()),
		"interval":                  int(flow.Interval.Seconds()),
	})
}

func (h *Handler) pollDeviceAuthRoute(w http.ResponseWriter, r *http.Request) {
	if !requireJSONContentType(w, r) {
		return
	}
	var body struct {
		DeviceCode string `json:"device_code"`
	}
	if err := decodeAllowedJSONBody(r, &body, map[string]bool{"device_code": true}, nil); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	if !h.requireAuthRateLimits(
		w,
		r,
		"device_token",
		body.DeviceCode,
		devicePollLimit,
		devicePollLimit,
		devicePollClientLimit,
		authShortWindow,
	) {
		return
	}
	result, err := h.store.PollDeviceAuthFlow(
		r.Context(),
		identitystore.DeviceAuthFlowPollInput{DeviceCode: body.DeviceCode},
	)
	if err != nil {
		h.writeAuthStorageError(w, r, err)
		return
	}
	if result.Status != identitystore.DeviceAuthFlowStatusApproved {
		status := http.StatusBadRequest
		if result.Status == identitystore.DeviceAuthFlowStatusPending ||
			result.Status == identitystore.DeviceAuthFlowStatusSlowDown {
			status = http.StatusAccepted
		}
		writeJSON(w, status, map[string]any{"error": string(result.Status), "interval": int(result.Interval.Seconds())})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"access_token": result.Token, "token_type": "Bearer"})
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
