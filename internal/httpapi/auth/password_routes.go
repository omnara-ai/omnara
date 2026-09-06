package auth

import (
	"context"
	"errors"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/authn"
	"github.com/omnara-ai/omnara/internal/emailaddr"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/storage/storeerr"
)

const (
	SignupLimit       = 5
	SignupClientLimit = 25
	LoginLimit        = 10
	LoginSubjectLimit = 25
	LoginClientLimit  = 100
	ResetLimit        = 5
	ResetClientLimit  = 25
	TokenConsumeLimit = 10
	TokenClientLimit  = 100

	authShortWindow       = 15 * time.Minute
	authLongWindow        = time.Hour
	minPublicAuthResponse = 150 * time.Millisecond
	revokeAllAuthTimeout  = time.Minute
	browserSessionTTL     = 30 * 24 * time.Hour
)

func (h *Handler) passwordSignupRoute(w http.ResponseWriter, r *http.Request) {
	if !h.signupEnabled {
		apierror.Write(w, openapi.ErrorCodeNotFound)
		return
	}
	defer padPublicAuthResponse(r.Context(), time.Now())
	if !h.requirePublicAuthJSON(w, r) {
		return
	}
	if h.email == nil {
		apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
		return
	}
	var body struct {
		Email    string `json:"email"`
		ReturnTo string `json:"return_to"`
	}
	if err := decodeAllowedJSONBody(r, &body, map[string]bool{"email": true, "return_to": true}, nil); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	email, normalizedEmail, validEmail := normalizeAuthEmail(body.Email)
	if !h.requireAuthRateLimits(
		w,
		r,
		"signup",
		normalizedEmail,
		SignupLimit,
		SignupLimit,
		SignupClientLimit,
		authLongWindow,
	) {
		return
	}
	if !validEmail {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
		return
	}
	record, err := h.store.StartPasswordSignup(
		r.Context(),
		identitystore.PasswordSignupStartInput{Email: email},
	)
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	if record.EmailAlreadyVerified {
		h.sendAuthEmail(r.Context(), func(ctx context.Context) error {
			return h.email.SendAccountExists(ctx, email, h.authURL("/login", nil))
		})
	} else {
		returnTo := safeSignupReturnTo(body.ReturnTo)
		verifyValues := url.Values{"token": []string{record.Token}}
		if returnTo != "/" {
			verifyValues.Set("return_to", returnTo)
		}
		verifyURL := h.authURL("/verify-email", verifyValues)
		h.sendAuthEmail(r.Context(), func(ctx context.Context) error {
			return h.email.SendEmailVerification(ctx, record.Email.Email, verifyURL)
		})
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
}

func safeSignupReturnTo(value string) string {
	if len(value) > 256 {
		return "/"
	}
	target, err := url.Parse(SafeReturnTo(value))
	if err != nil || target.Path != "/device" || target.Fragment != "" {
		return "/"
	}
	query, err := url.ParseQuery(target.RawQuery)
	if err != nil {
		return "/"
	}
	codes, ok := query["user_code"]
	if !ok || len(query) != 1 || len(codes) != 1 {
		return "/"
	}
	code, ok := identitystore.CanonicalDeviceUserCode(codes[0])
	if !ok {
		return "/"
	}
	return "/device?" + url.Values{"user_code": []string{code}}.Encode()
}

func (h *Handler) resendEmailVerificationRoute(w http.ResponseWriter, r *http.Request) {
	h.passwordSignupRoute(w, r)
}

func (h *Handler) completeEmailVerificationRoute(w http.ResponseWriter, r *http.Request) {
	if !h.signupEnabled {
		apierror.Write(w, openapi.ErrorCodeNotFound)
		return
	}
	defer padPublicAuthResponse(r.Context(), time.Now())
	if !h.requirePublicAuthJSON(w, r) {
		return
	}
	var body struct {
		Token       string `json:"token"`
		Password    string `json:"password"`
		DisplayName string `json:"display_name"`
	}
	if err := decodeAllowedJSONBody(
		r,
		&body,
		map[string]bool{"token": true, "password": true, "display_name": true},
		nil,
	); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	if !h.requireAuthRateLimits(
		w,
		r,
		"email_verify",
		body.Token,
		TokenConsumeLimit,
		TokenConsumeLimit,
		TokenClientLimit,
		authShortWindow,
	) {
		return
	}
	email, err := h.store.ActiveAuthTokenEmail(
		r.Context(),
		body.Token,
		identitystore.UserAuthTokenPurposeEmailVerification,
	)
	if err != nil {
		h.writeAuthTokenStorageError(w, r, err)
		return
	}
	passwordHash, ok := passwordHashForNewPassword(w, body.Password, email)
	if !ok {
		return
	}
	sessionToken, csrfToken, err := newBrowserSessionTokens()
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	record, err := h.store.CompletePasswordSignup(r.Context(), identitystore.CompletePasswordSignupInput{
		Token:            body.Token,
		PasswordHash:     passwordHash,
		DisplayName:      body.DisplayName,
		SessionToken:     sessionToken,
		SessionCSRFToken: csrfToken,
		SessionTTL:       browserSessionTTL,
	})
	if err != nil {
		h.writeAuthTokenStorageError(w, r, err)
		return
	}
	if !record.Verified {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}
	SetBrowserSessionCookies(w, r, h.publicURL, sessionToken, csrfToken, browserSessionTTL)
	setLastLoginMethodCookie(w, r, h.publicURL, "password", time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) passwordLoginRoute(w http.ResponseWriter, r *http.Request) {
	defer padPublicAuthResponse(r.Context(), time.Now())
	if !h.requirePublicAuthJSON(w, r) {
		return
	}
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := decodeAllowedJSONBody(r, &body, map[string]bool{"email": true, "password": true}, nil); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	email, normalizedEmail, _ := normalizeAuthEmail(body.Email)
	if !h.requireAuthRateLimits(
		w,
		r,
		"login",
		normalizedEmail,
		LoginLimit,
		LoginSubjectLimit,
		LoginClientLimit,
		authShortWindow,
	) {
		return
	}
	if email == "" {
		email = body.Email
	}
	sessionToken, csrfToken, err := newBrowserSessionTokens()
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	if _, err := h.store.AuthenticatePasswordAndCreateSession(r.Context(), identitystore.PasswordLoginSessionInput{
		Email:            email,
		Password:         body.Password,
		SessionToken:     sessionToken,
		SessionCSRFToken: csrfToken,
		SessionTTL:       browserSessionTTL,
	}); err != nil {
		h.writeAuthStorageError(w, r, err)
		return
	}
	SetBrowserSessionCookies(w, r, h.publicURL, sessionToken, csrfToken, browserSessionTTL)
	setLastLoginMethodCookie(w, r, h.publicURL, "password", time.Now().UTC())
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) requestPasswordResetRoute(w http.ResponseWriter, r *http.Request) {
	if !h.resetEnabled {
		apierror.Write(w, openapi.ErrorCodeNotFound)
		return
	}
	defer padPublicAuthResponse(r.Context(), time.Now())
	if !h.requirePublicAuthJSON(w, r) {
		return
	}
	if h.email == nil {
		apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
		return
	}
	var body struct {
		Email string `json:"email"`
	}
	if err := decodeAllowedJSONBody(r, &body, map[string]bool{"email": true}, nil); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	email, normalizedEmail, validEmail := normalizeAuthEmail(body.Email)
	if !h.requireAuthRateLimits(
		w,
		r,
		"password_reset_request",
		normalizedEmail,
		ResetLimit,
		ResetLimit,
		ResetClientLimit,
		authLongWindow,
	) {
		return
	}
	if !validEmail {
		writeJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
		return
	}
	record, err := h.store.StartPasswordReset(
		r.Context(),
		identitystore.PasswordResetStartInput{Email: email},
	)
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	if record.Found {
		resetURL := h.authURL("/reset-password", url.Values{"token": []string{record.Token}})
		h.sendAuthEmail(r.Context(), func(ctx context.Context) error {
			return h.email.SendPasswordReset(ctx, record.Email, resetURL)
		})
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "ok"})
}

func (h *Handler) completePasswordResetRoute(w http.ResponseWriter, r *http.Request) {
	if !h.resetEnabled {
		apierror.Write(w, openapi.ErrorCodeNotFound)
		return
	}
	defer padPublicAuthResponse(r.Context(), time.Now())
	if !h.requirePublicAuthJSON(w, r) {
		return
	}
	var body struct {
		Token    string `json:"token"`
		Password string `json:"password"`
	}
	if err := decodeAllowedJSONBody(r, &body, map[string]bool{"token": true, "password": true}, nil); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	if !h.requireAuthRateLimits(
		w,
		r,
		"password_reset",
		body.Token,
		TokenConsumeLimit,
		TokenConsumeLimit,
		TokenClientLimit,
		authShortWindow,
	) {
		return
	}
	email, err := h.store.ActiveAuthTokenEmail(
		r.Context(),
		body.Token,
		identitystore.UserAuthTokenPurposePasswordReset,
	)
	if err != nil {
		h.writeAuthTokenStorageError(w, r, err)
		return
	}
	passwordHash, ok := passwordHashForNewPassword(w, body.Password, email)
	if !ok {
		return
	}
	sessionToken, csrfToken, err := newBrowserSessionTokens()
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	user, err := h.store.CompletePasswordReset(r.Context(), identitystore.CompletePasswordResetInput{
		Token:            body.Token,
		PasswordHash:     passwordHash,
		SessionToken:     sessionToken,
		SessionCSRFToken: csrfToken,
		SessionTTL:       browserSessionTTL,
	})
	if err != nil {
		h.writeAuthTokenStorageError(w, r, err)
		return
	}
	SetBrowserSessionCookies(w, r, h.publicURL, sessionToken, csrfToken, browserSessionTTL)
	setLastLoginMethodCookie(w, r, h.publicURL, "password", time.Now().UTC())
	if h.email != nil {
		email, _, err := h.store.PrimaryVerifiedEmailForUser(r.Context(), user.ID)
		if err != nil {
			if h.log != nil {
				h.log.WarnContext(r.Context(), "password reset notice lookup failed", "error", err)
			}
		} else if email.Email != "" {
			h.sendAuthEmail(r.Context(), func(ctx context.Context) error {
				return h.email.SendPasswordChangedNotice(ctx, email.Email)
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) changePasswordRoute(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.currentPrincipal(r.Context())
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.BrowserSessionID == storage.NilID {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	if !h.requireAuthRateLimits(
		w,
		r,
		"password_change",
		principal.ID.String(),
		ResetLimit,
		ResetLimit,
		ResetClientLimit,
		authLongWindow,
	) {
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
	}
	if err := decodeAllowedJSONBody(
		r,
		&body,
		map[string]bool{"current_password": true, "new_password": true},
		nil,
	); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	email, _, err := h.store.PrimaryVerifiedEmailForUser(r.Context(), principal.ID)
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	passwordHash, ok := passwordHashForNewPassword(w, body.NewPassword, email.Email)
	if !ok {
		return
	}
	sessionToken, csrfToken, err := newBrowserSessionTokens()
	if err != nil {
		h.writeAuthServerError(w, r, err)
		return
	}
	if _, err := h.store.ChangePassword(r.Context(), identitystore.ChangePasswordInput{
		UserID:           principal.ID,
		CurrentPassword:  body.CurrentPassword,
		PasswordHash:     passwordHash,
		SessionToken:     sessionToken,
		SessionCSRFToken: csrfToken,
		SessionTTL:       browserSessionTTL,
	}); err != nil {
		h.writeAuthStorageError(w, r, err)
		return
	}
	SetBrowserSessionCookies(w, r, h.publicURL, sessionToken, csrfToken, browserSessionTTL)
	if h.email != nil && email.Email != "" {
		h.sendAuthEmail(r.Context(), func(ctx context.Context) error {
			return h.email.SendPasswordChangedNotice(ctx, email.Email)
		})
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) revokeAllAuthTokensRoute(w http.ResponseWriter, r *http.Request) {
	principal, ok := h.currentPrincipal(r.Context())
	if !ok || principal.Type != identitystore.PrincipalTypeUser || principal.BrowserSessionID == storage.NilID {
		apierror.Write(w, openapi.ErrorCodeForbidden)
		return
	}
	if !h.requireAuthRateLimits(
		w,
		r,
		"revoke_all",
		principal.ID.String(),
		ResetLimit,
		ResetLimit,
		ResetClientLimit,
		authLongWindow,
	) {
		return
	}
	var body struct {
		CurrentPassword string `json:"current_password"`
	}
	if err := decodeAllowedJSONBody(r, &body, map[string]bool{"current_password": true}, nil); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), revokeAllAuthTimeout)
	defer cancel()
	if err := h.compromiseRevoker.RevokeUserTokensForCompromiseWithPasswordIfPresent(
		ctx,
		principal.ID,
		body.CurrentPassword,
	); err != nil {
		h.writeAuthStorageError(w, r, err)
		return
	}
	clearBrowserSessionCookies(w, r, h.publicURL)
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func (h *Handler) requirePublicAuthJSON(w http.ResponseWriter, r *http.Request) bool {
	if !requireJSONContentType(w, r) {
		return false
	}
	if !SameOrigin(h.publicURL, r) {
		apierror.Write(w, openapi.ErrorCodeCsrfCheckFailed)
		return false
	}
	return true
}

func passwordHashForNewPassword(w http.ResponseWriter, password, email string) (string, bool) {
	if err := authn.ValidateNewPassword(password, email); err != nil {
		apierror.Write(w, openapi.ErrorCodeValidationFailed, err.Error())
		return "", false
	}
	passwordHash, err := authn.HashPassword(password)
	if err != nil {
		apierror.Write(w, openapi.ErrorCodeInternalError, "password hashing failed")
		return "", false
	}
	return passwordHash, true
}

func normalizeAuthEmail(email string) (string, string, bool) {
	trimmed := strings.TrimSpace(email)
	parsed, err := mail.ParseAddress(trimmed)
	if err != nil || parsed.Address != trimmed {
		return "", emailaddr.Normalize(email), false
	}
	return parsed.Address, emailaddr.Normalize(parsed.Address), true
}

func padPublicAuthResponse(ctx context.Context, start time.Time) {
	if remaining := minPublicAuthResponse - time.Since(start); remaining > 0 {
		timer := time.NewTimer(remaining)
		defer timer.Stop()
		select {
		case <-timer.C:
		case <-ctx.Done():
		}
	}
}

func writeAuthUnauthorized(w http.ResponseWriter) {
	apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid email or password")
}

func (h *Handler) writeAuthStorageError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, storeerr.ErrUnauthorized) {
		writeAuthUnauthorized(w)
		return
	}
	if errors.Is(err, storeerr.ErrConflict) {
		apierror.Write(w, openapi.ErrorCodeConflict, err.Error())
		return
	}
	h.writeAuthServerError(w, r, err)
}

func (h *Handler) writeAuthTokenStorageError(w http.ResponseWriter, r *http.Request, err error) {
	if errors.Is(err, storeerr.ErrUnauthorized) {
		apierror.Write(w, openapi.ErrorCodeUnauthorized, "invalid or expired token")
		return
	}
	h.writeAuthServerError(w, r, err)
}

func (h *Handler) writeAuthServerError(w http.ResponseWriter, r *http.Request, err error) {
	if h.log != nil {
		h.log.ErrorContext(r.Context(), "auth request failed", "error", err)
	}
	apierror.Write(w, openapi.ErrorCodeInternalError, "auth request failed")
}

func (h *Handler) sendAuthEmail(parent context.Context, send func(context.Context) error) {
	parent = context.WithoutCancel(parent)
	go func() {
		ctx, cancel := context.WithTimeout(parent, 10*time.Second)
		defer cancel()
		if err := send(ctx); err != nil && h.log != nil {
			h.log.Warn("auth email failed", "error", err)
		}
	}()
}

func (h *Handler) authURL(path string, values url.Values) string {
	if values == nil {
		values = url.Values{}
	}
	rawPath := path
	if encoded := values.Encode(); encoded != "" {
		rawPath += "?" + encoded
	}
	base := strings.TrimRight(h.publicURL, "/")
	if base == "" {
		return rawPath
	}
	return base + rawPath
}

func newBrowserSessionTokens() (string, string, error) {
	sessionToken, err := RandomURLToken(32)
	if err != nil {
		return "", "", err
	}
	csrfToken, err := RandomURLToken(32)
	if err != nil {
		return "", "", err
	}
	return sessionToken, csrfToken, nil
}

func (h *Handler) currentPrincipal(ctx context.Context) (identitystore.PrincipalRecord, bool) {
	if h.principalFromContext == nil {
		return identitystore.PrincipalRecord{}, false
	}
	return h.principalFromContext(ctx)
}
