package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	nethttpmiddleware "github.com/oapi-codegen/nethttp-middleware"

	"github.com/omnara-ai/omnara/internal/events"
	"github.com/omnara-ai/omnara/internal/httpapi/apierror"
	httpauth "github.com/omnara-ai/omnara/internal/httpapi/auth"
	"github.com/omnara-ai/omnara/internal/httpapi/openapi"
	"github.com/omnara-ai/omnara/internal/interactionform"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/log/logent"
	"github.com/omnara-ai/omnara/internal/metrics"
	"github.com/omnara-ai/omnara/internal/model"
	"github.com/omnara-ai/omnara/internal/notifications"
	"github.com/omnara-ai/omnara/internal/publicid"
	"github.com/omnara-ai/omnara/internal/storage"
	"github.com/omnara-ai/omnara/internal/storage/artifactstore"
	"github.com/omnara-ai/omnara/internal/storage/executionstore"
	"github.com/omnara-ai/omnara/internal/storage/identitystore"
	"github.com/omnara-ai/omnara/internal/toolpermission"
)

var (
	httpTestOrgID         = testHTTPID(1)
	httpTestProjectID     = testHTTPID(2)
	httpTestAgentID       = testHTTPID(3)
	httpTestTurnID        = testHTTPID(4)
	httpTestModelContext  = testHTTPID(5)
	httpTestToolCallID    = testHTTPID(6)
	httpTestInteractionID = testHTTPID(7)
	httpTestArtifactID    = testHTTPID(8)
	httpTestInputID       = testHTTPID(10)
	httpTestActorID       = testHTTPID(13)
)

func testHTTPID(last byte) storage.ID {
	return uuid.UUID{0x01, 0x92, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, last}
}

func testPublicID(t *testing.T, kind publicid.Kind, id storage.ID) string {
	t.Helper()
	value, err := publicid.Encode(kind, id)
	if err != nil {
		t.Fatalf("encode public id: %v", err)
	}
	return value
}

func mustNewUnitServer(t *testing.T, opts ...Option) *Server {
	t.Helper()
	serverOpts := append(
		[]Option{
			WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
			WithAgentToolCallUpdateSubscriber(noopAgentNotificationSubscriber{}),
			WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
		},
		opts...,
	)
	server, err := New(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		nil,
		serverOpts...)
	if err != nil {
		t.Fatalf("create http api server: %v", err)
	}
	return server
}

func TestNewRejectsIncompleteDaemonNotificationConfiguration(t *testing.T) {
	subscriber := noopDaemonWakeupSubscriber{}
	presence := noopDaemonPresenceStore{}
	for _, tc := range []struct {
		name   string
		store  *storage.Store
		option Option
	}{
		{
			name:   "subscriber",
			store:  &storage.Store{},
			option: WithDaemonNotifications(nil, presence, uuid.New()),
		},
		{
			name:   "presence",
			store:  &storage.Store{},
			option: WithDaemonNotifications(subscriber, nil, uuid.New()),
		},
		{
			name:   "replica id",
			store:  &storage.Store{},
			option: WithDaemonNotifications(subscriber, presence, uuid.Nil),
		},
		{
			name:   "store",
			option: WithDaemonNotifications(subscriber, presence, uuid.New()),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := New(
				slog.New(slog.NewTextHandler(io.Discard, nil)),
				tc.store,
				WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
				WithAgentToolCallUpdateSubscriber(noopAgentNotificationSubscriber{}),
				WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
				tc.option,
			)
			if err == nil {
				t.Fatal("New accepted incomplete daemon notification configuration")
			}
		})
	}
}

func TestAppHandlerServesHealthz(t *testing.T) {
	server := mustNewUnitServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if got := strings.TrimSpace(rec.Body.String()); got != `{"status":"ok"}` {
		t.Fatalf("healthz body = %q", got)
	}
	if got := rec.Header().Get("Cache-Control"); got != "no-store" {
		t.Fatalf("healthz Cache-Control = %q", got)
	}
}

func TestAppHandlerHealthzSubpathIsNotFound(t *testing.T) {
	server := mustNewUnitServer(t)
	req := httptest.NewRequest(http.MethodGet, "/healthz/db", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAppHandlerDoesNotServeReadyz(t *testing.T) {
	server := mustNewUnitServer(t)
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d", rec.Code)
	}
}

func TestAppHandlerRootIsEmpty404WithCSP(t *testing.T) {
	server := mustNewUnitServer(t)
	handler := server.Handler()

	rootReq := httptest.NewRequest(http.MethodGet, "/", nil)
	rootRec := httptest.NewRecorder()
	handler.ServeHTTP(rootRec, rootReq)
	if rootRec.Code != http.StatusNotFound || rootRec.Body.Len() != 0 {
		t.Fatalf("root response status=%d body=%q, want empty 404", rootRec.Code, rootRec.Body.String())
	}
	if got := rootRec.Header().Get("Content-Security-Policy"); got != "default-src 'none'; connect-src 'self'" {
		t.Fatalf("root CSP = %q", got)
	}

	unknownReq := httptest.NewRequest(http.MethodGet, "/does-not-exist", nil)
	unknownRec := httptest.NewRecorder()
	handler.ServeHTTP(unknownRec, unknownReq)
	if unknownRec.Code != http.StatusNotFound || !strings.Contains(unknownRec.Body.String(), `"error":"not found"`) {
		t.Fatalf(
			"unknown route response status=%d body=%q, want JSON not found",
			unknownRec.Code,
			unknownRec.Body.String(),
		)
	}
}

func TestMetricsEndpointExposesRouteMetricsWithoutAuth(t *testing.T) {
	set := metrics.New()
	server := mustNewUnitServer(t, WithHTTPRecorder(metrics.NewHTTPRecorder(set, metrics.SubsystemAPI)))
	handler := server.Handler()
	metricsHandler := metrics.ServerHandler(set, nil)

	orgPath := testPublicID(t, publicid.KindOrganization, httpTestOrgID)
	protectedReq := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/"+orgPath+"/projects", strings.NewReader(`{}`))
	protectedRec := httptest.NewRecorder()
	handler.ServeHTTP(protectedRec, protectedReq)
	if protectedRec.Code != http.StatusUnauthorized {
		t.Fatalf("protected status=%d want=%d", protectedRec.Code, http.StatusUnauthorized)
	}

	metricsReq := httptest.NewRequest(http.MethodGet, metrics.ScrapePath, nil)
	metricsRec := httptest.NewRecorder()
	metricsHandler.ServeHTTP(metricsRec, metricsReq)
	if metricsRec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d want=%d body=%s", metricsRec.Code, http.StatusOK, metricsRec.Body.String())
	}
	body := metricsRec.Body.String()
	for _, want := range []string{
		`go_goroutines`,
		`omnara_api_requests_total`,
		`code="401"`,
		`route="/api/v1/orgs/{orgID}/projects"`,
		`omnara_api_request_duration_seconds_bucket`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, body)
		}
	}
	if strings.Contains(body, `route="`+metrics.ScrapePath+`"`) {
		t.Fatalf("metrics endpoint should not count its own scrape:\n%s", body)
	}
}

func TestWebConfigRoute(t *testing.T) {
	tests := []struct {
		name           string
		opts           []Option
		wantBillingURL string
		wantAPIURL     string
	}{
		{
			name: "public config set",
			opts: []Option{
				WithBillingURL("https://billing.omnara.test/credits/"),
				WithPublicAPIURL("https://api.omnara.test/v1/"),
			},
			wantBillingURL: "https://billing.omnara.test/credits",
			wantAPIURL:     "https://api.omnara.test/v1",
		},
		{name: "unset values omitted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			server := mustNewUnitServer(t, tt.opts...)
			rec := httptest.NewRecorder()
			server.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, webConfigPath, nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("web config status = %d, want %d", rec.Code, http.StatusOK)
			}
			var body map[string]any
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatalf("decode web config: %v", err)
			}
			for key, want := range map[string]string{
				"billing_url": tt.wantBillingURL,
				"api_url":     tt.wantAPIURL,
			} {
				got, ok := body[key]
				if want == "" {
					if ok {
						t.Fatalf("web config includes %s when unset: %v", key, body)
					}
					continue
				}
				if got != want {
					t.Fatalf("%s = %v, want %q", key, got, want)
				}
			}
		})
	}
}

func TestAuthProtectsOpenAPIRoutes(t *testing.T) {
	server := mustNewUnitServer(t)
	handler := server.Handler()

	orgPath := testPublicID(t, publicid.KindOrganization, httpTestOrgID)
	protectedReq := httptest.NewRequest(http.MethodGet, "/api/v1/orgs/"+orgPath, nil)
	protectedRec := httptest.NewRecorder()
	handler.ServeHTTP(protectedRec, protectedReq)
	if protectedRec.Code != http.StatusUnauthorized {
		t.Fatalf("v1 status=%d want=%d", protectedRec.Code, http.StatusUnauthorized)
	}
}

func TestAuthProtectsLogoutRoute(t *testing.T) {
	server := mustNewUnitServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/auth/logout", strings.NewReader(`{}`))
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("logout status=%d want=%d", rec.Code, http.StatusUnauthorized)
	}
}

func TestWithTrustedProxyCIDRsLogsInvalidCIDR(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, nil))

	server, err := New(
		log,
		nil,
		WithAgentEventWakeupSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentToolCallUpdateSubscriber(noopAgentNotificationSubscriber{}),
		WithAgentStreamDeltaSubscriber(noopAgentNotificationSubscriber{}),
		WithTrustedProxyCIDRs([]string{"10.0.0.0/24", "not-a-cidr"}),
	)
	if err != nil {
		t.Fatalf("create server: %v", err)
	}
	if len(server.trustedProxyNets) != 1 {
		t.Fatalf("trusted proxy CIDRs=%d, want 1", len(server.trustedProxyNets))
	}
	got := buf.String()
	if !strings.Contains(got, "ignoring invalid trusted proxy CIDR") || !strings.Contains(got, "not-a-cidr") {
		t.Fatalf("log output %q missing invalid CIDR warning", got)
	}
}

func newRequestEventCapture() (*bytes.Buffer, *slog.Logger) {
	var buf bytes.Buffer
	log := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(_ []string, attr slog.Attr) slog.Attr {
			switch attr.Key {
			case slog.TimeKey:
				return slog.Attr{}
			case slog.LevelKey:
				if level, ok := attr.Value.Any().(slog.Level); ok {
					attr.Value = slog.StringValue(strings.ToLower(level.String()))
				}
			case slog.MessageKey:
				attr.Key = "message"
			}
			return attr
		},
	}))
	return &buf, log
}

func decodeRequestEvent(t *testing.T, buf *bytes.Buffer) map[string]any {
	t.Helper()
	var event map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &event); err != nil {
		t.Fatalf("decode request event: %v\n%s", err, buf.String())
	}
	return event
}

func TestRequestLogEmitsWideHTTPEvent(t *testing.T) {
	buf, log := newRequestEventCapture()
	handler := requestLog(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logent.Org(r.Context(), identitystore.OrgRecord{ID: httpTestOrgID})
		w.WriteHeader(http.StatusTeapot)
		if _, err := w.Write([]byte("teapot")); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/test?ignored=true", nil)
	req.Header.Set("User-Agent", "request-log-test")
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTeapot {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusTeapot)
	}
	event := decodeRequestEvent(t, buf)
	for key, want := range map[string]any{
		"level":               "warn",
		"message":             "http.request",
		"event.name":          "http.request",
		"http.method":         http.MethodPost,
		"http.path":           "/api/v1/test",
		"http.user_agent":     "request-log-test",
		"http.status_code":    float64(http.StatusTeapot),
		"http.response_bytes": float64(len("teapot")),
		"org.id":              httpTestOrgID.String(),
	} {
		if got := event[key]; got != want {
			t.Fatalf("%s=%v want=%v in event %+v", key, got, want, event)
		}
	}
	if event["event.id"] == "" {
		t.Fatalf("event.id missing in event %+v", event)
	}
	if _, ok := event["event.duration"]; !ok {
		t.Fatalf("event.duration missing in event %+v", event)
	}
}

func TestRequestLogAttachesHandlerErrorOnErrorResponse(t *testing.T) {
	buf, log := newRequestEventCapture()
	handlerErr := apierror.ProjectScoped(context.Canceled)
	handler := requestLog(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAPIResponseErrorHandler(w, r, handlerErr)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusInternalServerError)
	}
	event := decodeRequestEvent(t, buf)
	for key, want := range map[string]any{
		"level":            "error",
		"error.message":    handlerErr.Error(),
		"http.status_code": float64(http.StatusInternalServerError),
	} {
		if got := event[key]; got != want {
			t.Fatalf("%s=%v want=%v in event %+v", key, got, want, event)
		}
	}
}

func TestRequestLogClassifiesCanceledHandlerError(t *testing.T) {
	const clientClosedTelemetryStatus = 499
	buf, log := newRequestEventCapture()
	set := metrics.New()
	handlerErr := apierror.FromCode(
		openapi.ErrorCodeServiceUnavailable,
		"mapped dependency failure",
	).WithCause(context.Canceled)
	mux := http.NewServeMux()
	mux.Handle("GET /api/v1/test", requestLog(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		openAPIResponseErrorHandler(w, r, handlerErr)
	})))
	handler := metrics.NewHTTPRecorder(set, metrics.SubsystemAPI).Middleware(mux)(mux)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusServiceUnavailable)
	}
	var body openapi.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v\n%s", err, rec.Body.String())
	}
	if body.Code != openapi.ErrorCodeServiceUnavailable {
		t.Fatalf("response code=%q want=%q", body.Code, openapi.ErrorCodeServiceUnavailable)
	}
	event := decodeRequestEvent(t, buf)
	for key, want := range map[string]any{
		"level":                      "info",
		"http.status_code":           float64(clientClosedTelemetryStatus),
		"http.response_bytes":        float64(rec.Body.Len()),
		"http.request.cancel_cause":  context.Canceled.Error(),
		"http.request.cancel_source": "request_context",
	} {
		if got := event[key]; got != want {
			t.Fatalf("%s=%v want=%v in event %+v", key, got, want, event)
		}
	}
	if _, ok := event["error.message"]; ok {
		t.Fatalf("canceled request has error.message in event %+v", event)
	}
	metricsRec := httptest.NewRecorder()
	set.Handler().ServeHTTP(metricsRec, httptest.NewRequest(http.MethodGet, metrics.ScrapePath, nil))
	metricsBody := metricsRec.Body.String()
	for _, want := range []string{
		`omnara_api_requests_total{code="499",method="get",route="/api/v1/test"} 1`,
		`omnara_api_request_duration_seconds_count{code="499",method="get",route="/api/v1/test"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("metrics output missing %q:\n%s", want, metricsBody)
		}
	}
	if strings.Contains(metricsBody, `code="503",method="get",route="/api/v1/test"`) {
		t.Fatalf("canceled request recorded as 503:\n%s", metricsBody)
	}
}

func TestRequestLogPreservesHandlerErrorWhenRequestIsCanceled(t *testing.T) {
	buf, log := newRequestEventCapture()
	handlerErr := apierror.FromCode(
		openapi.ErrorCodeServiceUnavailable,
		"mapped dependency failure",
	).WithCause(errors.New("query agent row: connection refused"))
	handler := requestLog(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logpkg.Error(r.Context(), context.Canceled)
		openAPIResponseErrorHandler(w, r, handlerErr)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusServiceUnavailable)
	}
	event := decodeRequestEvent(t, buf)
	for key, want := range map[string]any{
		"level":                      "error",
		"error.message":              errors.Join(context.Canceled, handlerErr).Error(),
		"http.status_code":           float64(http.StatusServiceUnavailable),
		"http.request.cancel_cause":  context.Canceled.Error(),
		"http.request.cancel_source": "request_context",
	} {
		if got := event[key]; got != want {
			t.Fatalf("%s=%v want=%v in event %+v", key, got, want, event)
		}
	}
}

func TestRequestLogPreservesHandlerPanicWhenRequestIsCanceled(t *testing.T) {
	buf, log := newRequestEventCapture()
	handler := requestLog(log)(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		logpkg.Error(r.Context(), context.Canceled)
		panic("boom")
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusInternalServerError)
	}
	event := decodeRequestEvent(t, buf)
	for key, want := range map[string]any{
		"level":                      "error",
		"error.message":              "context canceled\nhttp handler panicked: boom",
		"http.status_code":           float64(http.StatusInternalServerError),
		"http.request.cancel_cause":  context.Canceled.Error(),
		"http.request.cancel_source": "request_context",
	} {
		if got := event[key]; got != want {
			t.Fatalf("%s=%v want=%v in event %+v", key, got, want, event)
		}
	}
	stack, _ := event["error.stack"].(string)
	if !strings.Contains(stack, "TestRequestLogPreservesHandlerPanicWhenRequestIsCanceled") {
		t.Fatalf("error.stack missing panic origin in event %+v", event)
	}
}

func TestRequestLogClassifiesCanceledAuthenticationError(t *testing.T) {
	const clientClosedTelemetryStatus = 499
	buf, log := newRequestEventCapture()
	handler := requestLog(log)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		logent.AuthFailedError(
			r.Context(),
			logent.AuthSchemeBearer,
			logent.TokenKindPersonalAccess,
			logent.AuthResultUnavailable,
			context.Canceled,
		)
		apierror.Write(w, openapi.ErrorCodeAuthenticationUnavailable)
	}))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil).WithContext(ctx)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusServiceUnavailable)
	}
	event := decodeRequestEvent(t, buf)
	for key, want := range map[string]any{
		"level":                      "info",
		"auth.result":                string(logent.AuthResultUnavailable),
		"http.status_code":           float64(clientClosedTelemetryStatus),
		"http.request.cancel_cause":  context.Canceled.Error(),
		"http.request.cancel_source": "request_context",
	} {
		if got := event[key]; got != want {
			t.Fatalf("%s=%v want=%v in event %+v", key, got, want, event)
		}
	}
	if _, ok := event["error.message"]; ok {
		t.Fatalf("canceled request has error.message in event %+v", event)
	}
}

func TestRequestLogAttachesRequestErrorOnBadRequest(t *testing.T) {
	requestErr := errors.New("request body must contain a single JSON value")
	tests := []struct {
		name  string
		serve http.HandlerFunc
	}{
		{"strict request error", func(w http.ResponseWriter, r *http.Request) {
			openAPIRequestErrorHandler(w, r, requestErr)
		}},
		{"route binding error", func(w http.ResponseWriter, r *http.Request) {
			openAPIErrorHandler(w, r, requestErr)
		}},
		{"validation error", func(w http.ResponseWriter, r *http.Request) {
			openAPIValidationErrorHandler(r.Context(), requestErr, w, r, nethttpmiddleware.ErrorHandlerOpts{
				StatusCode: http.StatusBadRequest,
			})
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			buf, log := newRequestEventCapture()
			handler := requestLog(log)(tt.serve)

			req := httptest.NewRequest(http.MethodPost, "/api/v1/test", nil)
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)

			if rec.Code != http.StatusBadRequest {
				t.Fatalf("status=%d want=%d", rec.Code, http.StatusBadRequest)
			}
			event := decodeRequestEvent(t, buf)
			for key, want := range map[string]any{
				"level":            "warn",
				"error.message":    requestErr.Error(),
				"http.status_code": float64(http.StatusBadRequest),
			} {
				if got := event[key]; got != want {
					t.Fatalf("%s=%v want=%v in event %+v", key, got, want, event)
				}
			}
		})
	}
}

func TestRequestLogRecoversHandlerPanic(t *testing.T) {
	buf, log := newRequestEventCapture()
	handler := requestLog(log)(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status=%d want=%d", rec.Code, http.StatusInternalServerError)
	}
	var body openapi.Error
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode response body: %v\n%s", err, rec.Body.String())
	}
	if body.Code != openapi.ErrorCodeInternalError {
		t.Fatalf("response code=%q want=%q", body.Code, openapi.ErrorCodeInternalError)
	}
	event := decodeRequestEvent(t, buf)
	for key, want := range map[string]any{
		"level":            "error",
		"error.message":    "http handler panicked: boom",
		"http.status_code": float64(http.StatusInternalServerError),
	} {
		if got := event[key]; got != want {
			t.Fatalf("%s=%v want=%v in event %+v", key, got, want, event)
		}
	}
	stack, _ := event["error.stack"].(string)
	if !strings.Contains(stack, "TestRequestLogRecoversHandlerPanic") {
		t.Fatalf("error.stack missing panic origin in event %+v", event)
	}
}

func TestRequestLogAbortsPartialResponseOnHandlerPanic(t *testing.T) {
	buf, log := newRequestEventCapture()
	handler := requestLog(log)(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if _, err := w.Write([]byte("partial")); err != nil {
			t.Fatalf("write response: %v", err)
		}
		panic("boom")
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/test", nil)
	rec := httptest.NewRecorder()
	recovered := func() (recovered any) {
		defer func() { recovered = recover() }()
		handler.ServeHTTP(rec, req)
		return nil
	}()

	if err, ok := recovered.(error); !ok || !errors.Is(err, http.ErrAbortHandler) {
		t.Fatalf("recovered=%v want=%v", recovered, http.ErrAbortHandler)
	}
	event := decodeRequestEvent(t, buf)
	for key, want := range map[string]any{
		"level":            "error",
		"error.message":    "http handler panicked: boom",
		"http.status_code": float64(http.StatusOK),
	} {
		if got := event[key]; got != want {
			t.Fatalf("%s=%v want=%v in event %+v", key, got, want, event)
		}
	}
	stack, _ := event["error.stack"].(string)
	if !strings.Contains(stack, "TestRequestLogAbortsPartialResponseOnHandlerPanic") {
		t.Fatalf("error.stack missing panic origin in event %+v", event)
	}
}

func TestPublicIDEncodingInvariantReturnsHTTP500(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, err := publicID(publicid.KindAgent, storage.NilID); err != nil {
			apierror.Write(w, openapi.ErrorCodeInternalError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"id": "unreachable"})
	})
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500, got %d with body %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "invalid_") {
		t.Fatalf("public response leaked invalid public id sentinel: %s", rec.Body.String())
	}
}

func TestAgentInteractionResponseOmitsInternalPermissionAuthority(t *testing.T) {
	authorization, err := toolpermission.NewAuthorization(
		"set_integration_target",
		json.RawMessage(`{"target_ref":"slack-abcd"}`),
	)
	if err != nil {
		t.Fatalf("build permission authorization: %v", err)
	}
	mode, ok := toolpermission.FindMode(
		toolpermission.CommonModeDescriptors(),
		toolpermission.ModeAlwaysAsk,
	)
	if !ok {
		t.Fatal("always_ask permission mode missing")
	}
	value, err := toolpermission.NewAllowDenyForm(
		"Permission requested for set_integration_target",
		[]interactionform.ContextItem{{Label: "Target", Value: "slack-abcd"}},
	)
	if err != nil {
		t.Fatalf("build permission interaction form: %v", err)
	}
	permissionRequest, err := toolpermission.NewRequest(
		mode,
		toolpermission.DefaultSelection(toolpermission.ModeAlwaysAsk),
		authorization,
		value,
	)
	if err != nil {
		t.Fatalf("build permission request: %v", err)
	}
	requestJSON, err := json.Marshal(permissionRequest)
	if err != nil {
		t.Fatalf("marshal permission request: %v", err)
	}
	record := executionstore.AgentInteractionRecord{
		ID:                 httpTestInteractionID,
		ProjectID:          httpTestProjectID,
		AgentID:            httpTestAgentID,
		TurnID:             httpTestTurnID,
		ModelCallContextID: httpTestModelContext,
		ToolCallID:         httpTestToolCallID,
		InteractionKind:    "permission",
		State:              executionstore.AgentInteractionStateOpen,
		Request:            requestJSON,
		Resolution:         json.RawMessage(`{}`),
		CreatedAt:          time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC),
	}
	response, err := agentInteractionResponseFromRecord(httpTestOrgID, record)
	if err != nil {
		t.Fatalf("project response: %v", err)
	}
	body, err := json.Marshal(response)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	for _, field := range []string{"turn_id", "model_call_context_id"} {
		if _, ok := decoded[field]; ok {
			t.Fatalf("public response leaked %s: %s", field, string(body))
		}
	}
	request, ok := decoded["request"].(map[string]any)
	if !ok {
		t.Fatalf("request shape = %#v", decoded["request"])
	}
	if _, exposed := request["authorization"]; exposed {
		t.Fatalf("public response exposed internal authorization: %+v", request)
	}
	if request["title"] != "Permission requested for set_integration_target" {
		t.Fatalf("public response lost interaction form title: %+v", request)
	}
	if decoded["tool_name"] != "set_integration_target" {
		t.Fatalf("public response lost permission tool name: %+v", decoded)
	}
	if toolCallID, ok := decoded["tool_call_id"].(string); !ok || !strings.HasPrefix(toolCallID, "tcl_") {
		t.Fatalf("public response lost tool call id: %+v", decoded)
	}
	questions, ok := request["questions"].([]any)
	if !ok || len(questions) != 1 {
		t.Fatalf("public response lost permission question: %+v", request)
	}
}

func TestPublicEventResponseExposesModelCallContextOnlyForModelOutput(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 30, 0, 0, time.UTC)
	modelOutput, err := publicEventResponseFromReadRecord(executionstore.AgentEventReadRecord{
		ID:                 testHTTPID(21),
		OrgID:              httpTestOrgID,
		ProjectID:          httpTestProjectID,
		AgentID:            httpTestAgentID,
		TurnID:             httpTestTurnID,
		TurnSequence:       1,
		Sequence:           9,
		EventKind:          string(events.KindModelOutput),
		ModelCallContextID: httpTestModelContext,
		ModelStopReason:    model.StopReasonEndTurn,
		ContentBlocks:      json.RawMessage(`[]`),
		CreatedAt:          now,
	})
	if err != nil {
		t.Fatalf("model output event response: %v", err)
	}
	modelOutputEvent, err := modelOutput.AsModelOutputEvent()
	if err != nil {
		t.Fatalf("decode model output event: %v", err)
	}
	wantModelContextID := testPublicID(t, publicid.KindModelCallContext, httpTestModelContext)
	if modelOutputEvent.ModelCallContextId != wantModelContextID {
		t.Fatalf(
			"model_call_context_id = %v, want %q",
			modelOutputEvent.ModelCallContextId,
			wantModelContextID,
		)
	}

	agentInput, err := publicEventResponseFromReadRecord(executionstore.AgentEventReadRecord{
		ID:                 testHTTPID(22),
		OrgID:              httpTestOrgID,
		ProjectID:          httpTestProjectID,
		AgentID:            httpTestAgentID,
		TurnID:             httpTestTurnID,
		TurnSequence:       1,
		Sequence:           10,
		EventKind:          string(events.KindAgentInput),
		InputKind:          "content",
		ActorID:            httpTestActorID,
		AgentInputID:       httpTestInputID,
		ModelCallContextID: httpTestModelContext,
		ContentBlocks:      json.RawMessage(`[]`),
		CreatedAt:          now,
	})
	if err != nil {
		t.Fatalf("agent input event response: %v", err)
	}
	assertNoJSONField(t, agentInput, "model_call_context_id")
}

func TestPublicModelOutputEventAcceptsEveryDurableStopReason(t *testing.T) {
	reasons := []model.StopReason{
		model.StopReasonEndTurn,
		model.StopReasonToolUse,
		model.StopReasonMaxTokens,
		model.StopReasonRefusal,
		model.StopReasonContentFilter,
		model.StopReasonError,
	}
	for _, reason := range reasons {
		t.Run(string(reason), func(t *testing.T) {
			response, err := publicEventResponseFromReadRecord(executionstore.AgentEventReadRecord{
				ID:                 testHTTPID(26),
				OrgID:              httpTestOrgID,
				ProjectID:          httpTestProjectID,
				AgentID:            httpTestAgentID,
				TurnID:             httpTestTurnID,
				TurnSequence:       1,
				Sequence:           11,
				EventKind:          string(events.KindModelOutput),
				ModelCallContextID: httpTestModelContext,
				ModelStopReason:    reason,
				ContentBlocks:      json.RawMessage(`[{"type":"text","text":"durable output"}]`),
				CreatedAt:          time.Date(2026, 5, 8, 13, 0, 0, 0, time.UTC),
			})
			if err != nil {
				t.Fatalf("project %s model output: %v", reason, err)
			}
			output, err := response.AsModelOutputEvent()
			if err != nil {
				t.Fatalf("decode %s model output: %v", reason, err)
			}
			if string(output.StopReason) != string(reason) {
				t.Fatalf("stop reason = %q, want %q", output.StopReason, reason)
			}
		})
	}
}

func TestPublicResourceResponsesHideInternalExecutionAuthority(t *testing.T) {
	now := time.Date(2026, 5, 8, 12, 0, 0, 0, time.UTC)
	size := int64(42)
	agentResponse, err := publicAgentResponseFromRecord(executionstore.AgentRecord{
		ID:             httpTestAgentID,
		OrgID:          httpTestOrgID,
		ProjectID:      httpTestProjectID,
		State:          "active",
		IdempotencyKey: "idem_internal",
		CreatedAt:      now,
		UpdatedAt:      now,
	})
	if err != nil {
		t.Fatalf("agent response: %v", err)
	}
	artifactResponse, err := publicArtifactResponseFromRecord(httpTestOrgID, artifactstore.ArtifactRecord{
		ID:             httpTestArtifactID,
		ProjectID:      httpTestProjectID,
		AgentID:        httpTestAgentID,
		ContentType:    "text/plain",
		Filename:       "log.txt",
		Digest:         "sha256:test",
		SizeBytes:      &size,
		IdempotencyKey: "idem_internal",
		CreatedAt:      now,
	})
	if err != nil {
		t.Fatalf("artifact response: %v", err)
	}
	inputResponse, err := publicAgentInputResponseFromRecord(executionstore.AgentInputRecord{
		ID:                  httpTestInputID,
		ProjectID:           httpTestProjectID,
		AgentID:             httpTestAgentID,
		State:               "queued",
		ActorID:             httpTestActorID,
		InputKind:           "content",
		InputIdempotencyKey: "public-input-key",
		QueuedAt:            now,
		Metadata: json.RawMessage(
			`{"visible":true,"source_event_id":"evt_internal","operation_key":"op_internal"}`,
		),
	})
	if err != nil {
		t.Fatalf("agent input response: %v", err)
	}
	if inputResponse.InputIdempotencyKey == nil || *inputResponse.InputIdempotencyKey != "public-input-key" {
		t.Fatalf("agent input idempotency key = %v", inputResponse.InputIdempotencyKey)
	}
	nonContentInputResponse, err := publicAgentInputResponseFromRecord(executionstore.AgentInputRecord{
		ID:                  testHTTPID(27),
		ProjectID:           httpTestProjectID,
		AgentID:             httpTestAgentID,
		State:               "queued",
		InputKind:           "control",
		InputIdempotencyKey: "non-content-key",
		QueuedAt:            now,
	})
	if err != nil {
		t.Fatalf("non-content agent input response: %v", err)
	}
	if nonContentInputResponse.InputIdempotencyKey != nil {
		t.Fatalf(
			"non-content agent input idempotency key = %v",
			nonContentInputResponse.InputIdempotencyKey,
		)
	}
	eventResponses, err := publicEventResponsesFromReadRecords([]executionstore.AgentEventReadRecord{
		{
			ID:            testHTTPID(16),
			OrgID:         httpTestOrgID,
			ProjectID:     httpTestProjectID,
			AgentID:       httpTestAgentID,
			TurnID:        httpTestTurnID,
			TurnSequence:  1,
			Sequence:      4,
			EventKind:     string(events.KindToolResult),
			ToolCallID:    httpTestToolCallID,
			ToolOutcome:   executionstore.ToolResultOutcomeSucceeded,
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"ok"}]`),
			CreatedAt:     now,
		},
		{
			ID:            testHTTPID(17),
			OrgID:         httpTestOrgID,
			ProjectID:     httpTestProjectID,
			AgentID:       httpTestAgentID,
			TurnID:        httpTestTurnID,
			TurnSequence:  1,
			Sequence:      5,
			EventKind:     string(events.KindAgentInput),
			InputKind:     "content",
			ActorID:       httpTestActorID,
			AgentInputID:  httpTestInputID,
			ContentBlocks: json.RawMessage(`[{"type":"text","text":"input"}]`),
			CreatedAt:     now,
		},
		{
			ID:                  testHTTPID(18),
			OrgID:               httpTestOrgID,
			ProjectID:           httpTestProjectID,
			AgentID:             httpTestAgentID,
			TurnID:              httpTestTurnID,
			TurnSequence:        1,
			Sequence:            6,
			EventKind:           string(events.KindAgentInput),
			InputKind:           "control",
			InputIdempotencyKey: "non-content-key",
			ControlType:         "cancel_current",
			ActorID:             httpTestActorID,
			AgentInputID:        testHTTPID(23),
			ContentBlocks:       json.RawMessage(`[]`),
			CreatedAt:           now,
		},
		{
			ID:                 testHTTPID(19),
			OrgID:              httpTestOrgID,
			ProjectID:          httpTestProjectID,
			AgentID:            httpTestAgentID,
			TurnID:             httpTestTurnID,
			TurnSequence:       1,
			Sequence:           7,
			EventKind:          string(events.KindModelOutput),
			ModelCallContextID: httpTestModelContext,
			ModelStopReason:    model.StopReasonEndTurn,
			ContentBlocks:      json.RawMessage(`[{"type":"text","text":"output"}]`),
			CreatedAt:          now,
		},
		{
			ID:                  testHTTPID(24),
			OrgID:               httpTestOrgID,
			ProjectID:           httpTestProjectID,
			AgentID:             httpTestAgentID,
			TurnID:              httpTestTurnID,
			TurnSequence:        1,
			Sequence:            8,
			EventKind:           string(events.KindAgentInput),
			InputKind:           "interaction_response",
			ActorID:             httpTestActorID,
			AgentInputID:        testHTTPID(25),
			TargetInteractionID: httpTestInteractionID,
			ContentBlocks:       json.RawMessage(`[]`),
			CreatedAt:           now,
		},
	})
	if err != nil {
		t.Fatalf("event responses: %v", err)
	}
	toolResultEvent, err := eventResponses[0].AsToolResultEvent()
	if err != nil {
		t.Fatalf("decode tool result event: %v", err)
	}
	if toolResultEvent.Outcome != openapi.ToolCallOutcomeSucceeded {
		t.Fatalf("tool result event outcome = %s", toolResultEvent.Outcome)
	}
	controlEvent, err := eventResponses[2].AsAgentInputEvent()
	if err != nil {
		t.Fatalf("decode control event: %v", err)
	}
	if controlEvent.InputIdempotencyKey != nil {
		t.Fatalf("control event idempotency key = %v", controlEvent.InputIdempotencyKey)
	}
	interactionResponseEvent, err := eventResponses[4].AsAgentInputEvent()
	if err != nil {
		t.Fatalf("decode interaction response event: %v", err)
	}
	wantInteractionID := testPublicID(
		t,
		publicid.KindAgentInteraction,
		httpTestInteractionID,
	)
	if interactionResponseEvent.InteractionId == nil ||
		*interactionResponseEvent.InteractionId != wantInteractionID {
		t.Fatalf(
			"interaction_id = %v, want %q",
			interactionResponseEvent.InteractionId,
			wantInteractionID,
		)
	}
	for _, eventResponse := range eventResponses {
		kind, err := eventResponse.Discriminator()
		if err != nil {
			t.Fatalf("decode event discriminator: %v", err)
		}
		if kind != string(events.KindAgentInput) {
			continue
		}
		assertNoJSONField(t, eventResponse, "payload")
	}
	responses := []any{agentResponse, artifactResponse, inputResponse, eventResponses}

	for _, response := range responses {
		body, err := json.Marshal(response)
		if err != nil {
			t.Fatalf("marshal response: %v", err)
		}
		assertPublicJSONDoesNotContain(t, body,
			`"idempotency_key"`,
			"operation_fingerprint",
			"operation_key",
			"previous_backlog_position",
			"provider_call_id",
			"provider_metadata",
			"provider_operation_id",
			"provider_response_id",
			"producer_runtime_generation",
			"producer_runtime_lock_id",
			"storage_provider",
			"storage_key",
			"storage_handle",
			"runtime_lock_id",
			"machine_connection_id",
			"lease_id",
			"connector_installation_id",
			"source_event_id",
			"process_id",
			"new_backlog_position",
			"mctx_internal",
			"mcn_internal",
			"postgres://artifact_blobs",
			"pop_internal",
			"ddp_nested",
			"lse_nested",
			"op_internal",
			"resp_internal",
			"tcl_internal",
		)
		if bytes.Contains(body, []byte(`"structured_data"`)) &&
			(!bytes.Contains(body, []byte(`"raw":"user raw value"`)) ||
				!bytes.Contains(body, []byte(`"raw":"payload raw value"`))) {
			t.Fatalf("public projection must preserve legitimate raw content fields: %s", string(body))
		}
	}
}

func TestPublicContentBlocksRewritesArtifactReferenceIDs(t *testing.T) {
	publicArtifactID := testPublicID(t, publicid.KindArtifact, httpTestArtifactID)
	blocks, err := publicToolResultContentBlocks(json.RawMessage(
		`[{"type":"media_ref","artifact_id":"` + httpTestArtifactID.String() +
			`","exclude_from_model_context":true,"metadata":{"source":"test"}}]`,
	))
	if err != nil {
		t.Fatalf("project public content blocks: %v", err)
	}
	block, err := blocks[0].AsMediaRefContentBlock()
	if err != nil {
		t.Fatalf("decode media reference block: %v", err)
	}
	if block.ArtifactId != publicArtifactID {
		t.Fatalf("artifact_id = %v, want %s", block.ArtifactId, publicArtifactID)
	}
	if block.ExcludeFromModelContext == nil || !*block.ExcludeFromModelContext {
		t.Fatalf("exclude_from_model_context = %v, want true", block.ExcludeFromModelContext)
	}
	if block.Metadata["source"] != "test" {
		t.Fatalf("metadata = %v, want source=test", block.Metadata)
	}
}

func TestPublicContentBlocksRewritesToolCallReferenceIDs(t *testing.T) {
	publicToolCallID := testPublicID(t, publicid.KindToolCall, httpTestToolCallID)
	blocks, err := publicModelOutputContentBlocks(json.RawMessage(
		`[{"type":"tool_call","tool_call_id":"` + httpTestToolCallID.String() +
			`","tool_type":"built_in","name":"lookup_customer","input":{}}]`,
	))
	if err != nil {
		t.Fatalf("project public content blocks: %v", err)
	}
	block, err := blocks[0].AsModelToolCallContentBlock()
	if err != nil {
		t.Fatalf("decode tool call block: %v", err)
	}
	if block.ToolCallId != publicToolCallID {
		t.Fatalf("tool_call_id = %v, want %s", block.ToolCallId, publicToolCallID)
	}
}

func TestPublicContentBlocksPreservesStructuredDataByFieldName(t *testing.T) {
	blocks, err := publicToolResultContentBlocks(json.RawMessage(
		`[{"type":"structured_data","value":{"process_id":"domain-process","operation_key":"domain-operation"}}]`,
	))
	if err != nil {
		t.Fatalf("project public content blocks: %v", err)
	}
	body, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal public content blocks: %v", err)
	}
	if got := string(body); !strings.Contains(got, `"process_id":"domain-process"`) ||
		!strings.Contains(got, `"operation_key":"domain-operation"`) {
		t.Fatalf("structured data was rewritten: %s", got)
	}
}

func TestPublicContentBlocksPreserveEveryStructuredJSONType(t *testing.T) {
	raw := json.RawMessage(
		`[{"type":"structured_data","value":{"answer":9007199254740993}},{"type":"structured_data","value":["first",2,false,null]},{"type":"structured_data","value":"plain string"},{"type":"structured_data","value":17.5},{"type":"structured_data","value":true},{"type":"structured_data","value":null}]`,
	)
	blocks, err := publicToolResultContentBlocks(raw)
	if err != nil {
		t.Fatalf("project public content blocks: %v", err)
	}
	body, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal public content blocks: %v", err)
	}
	if string(body) != string(raw) {
		t.Fatalf("structured data round trip = %s, want %s", body, raw)
	}
}

func TestPublicToolCallInputPreservesLargeJSONIntegers(t *testing.T) {
	raw := json.RawMessage(
		`[{"type":"tool_call","tool_call_id":"` + httpTestToolCallID.String() +
			`","tool_type":"built_in","name":"lookup_customer","input":{"account_id":9007199254740993}}]`,
	)
	blocks, err := publicModelOutputContentBlocks(raw)
	if err != nil {
		t.Fatalf("project public content blocks: %v", err)
	}
	body, err := json.Marshal(blocks)
	if err != nil {
		t.Fatalf("marshal public content blocks: %v", err)
	}
	if !bytes.Contains(body, []byte(`"account_id":9007199254740993`)) {
		t.Fatalf("tool input lost numeric precision: %s", body)
	}
}

func TestPublicContentBlocksRejectMisplacedFields(t *testing.T) {
	tests := []struct {
		name    string
		project func(json.RawMessage) error
		raw     json.RawMessage
	}{
		{
			name: "agent input provider replay",
			project: func(raw json.RawMessage) error {
				_, err := publicAgentInputContentBlocks(raw)
				return err
			},
			raw: json.RawMessage(
				`[{"type":"text","text":"hello","provider_replay":{"opaque":true}}]`,
			),
		},
		{
			name: "model output tool field on text",
			project: func(raw json.RawMessage) error {
				_, err := publicModelOutputContentBlocks(raw)
				return err
			},
			raw: json.RawMessage(
				`[{"type":"text","text":"hello","tool_call_id":"00000000-0000-4000-8000-000000000001"}]`,
			),
		},
		{
			name: "model output nonobject tool input",
			project: func(raw json.RawMessage) error {
				_, err := publicModelOutputContentBlocks(raw)
				return err
			},
			raw: json.RawMessage(
				`[{"type":"tool_call","tool_call_id":"` + httpTestToolCallID.String() +
					`","tool_type":"built_in","name":"lookup_customer","input":null}]`,
			),
		},
		{
			name: "tool result provider item",
			project: func(raw json.RawMessage) error {
				_, err := publicToolResultContentBlocks(raw)
				return err
			},
			raw: json.RawMessage(
				`[{"type":"structured_data","value":{"ok":true},"provider_item_id":"item_1"}]`,
			),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.project(test.raw); err == nil {
				t.Fatalf("misplaced content block field was accepted: %s", test.raw)
			}
		})
	}
}

func assertPublicJSONDoesNotContain(t *testing.T, body []byte, forbidden ...string) {
	t.Helper()
	text := string(body)
	for _, value := range forbidden {
		if strings.Contains(text, value) {
			t.Fatalf("public response leaked %q: %s", value, text)
		}
	}
}

func TestAuthProtectsAPI(t *testing.T) {
	server := mustNewUnitServer(t)
	handler := server.Handler()

	req := httptest.NewRequest(
		http.MethodPost,
		"/api/v1/orgs/"+testPublicID(
			t,
			publicid.KindOrganization,
			httpTestOrgID,
		)+"/projects/"+testPublicID(
			t,
			publicid.KindProject,
			httpTestProjectID,
		)+"/agents",
		nil,
	)
	rec := httptest.NewRecorder()
	handler.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized without token, got %d", rec.Code)
	}

}

func TestAPIAuthIsClosedByDefault(t *testing.T) {
	server := mustNewUnitServer(t)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/orgs/org_test/projects/prj_test/agents", nil)
	rec := httptest.NewRecorder()

	server.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected unauthorized by default, got %d", rec.Code)
	}
}

func TestFlattenedRouteTableMatchesOnlyExactNestedRoutes(t *testing.T) {
	server := mustNewUnitServer(t)
	mux := http.NewServeMux()
	server.registerRoutes(mux)
	handler := maxBody(requestBodyLimit)(mux)
	orgPath := testPublicID(t, publicid.KindOrganization, httpTestOrgID)
	projectPath := testPublicID(t, publicid.KindProject, httpTestProjectID)
	agentPath := testPublicID(t, publicid.KindAgent, httpTestAgentID)
	turnPath := testPublicID(t, publicid.KindAgentTurn, httpTestTurnID)
	nestedAgentPath := testPublicID(t, publicid.KindAgent, testHTTPID(20))
	agentProfilePath := testPublicID(t, publicid.KindAgentProfile, testHTTPID(25))
	agentConfigPath := testPublicID(t, publicid.KindAgentConfig, testHTTPID(28))
	interactionPath := testPublicID(t, publicid.KindAgentInteraction, httpTestInteractionID)
	inputPath := testPublicID(t, publicid.KindAgentInput, httpTestInputID)
	toolCallPath := testPublicID(t, publicid.KindToolCall, testHTTPID(33))
	artifactPath := testPublicID(t, publicid.KindArtifact, httpTestArtifactID)
	machinePath := testPublicID(t, publicid.KindMachine, testHTTPID(21))
	tokenPath := testPublicID(t, publicid.KindMachineDaemonToken, testHTTPID(22))
	runtimePath := testPublicID(t, publicid.KindDaemonRuntime, testHTTPID(23))
	grantPath := testPublicID(t, publicid.KindProjectMachineGrant, testHTTPID(24))
	poolPath := testPublicID(t, publicid.KindMachinePool, testHTTPID(26))
	poolGrantPath := testPublicID(t, publicid.KindProjectMachinePoolGrant, testHTTPID(27))
	secretPath := testPublicID(t, publicid.KindSecret, testHTTPID(29))
	modelProviderConfigPath := testPublicID(t, publicid.KindModelProviderConfig, testHTTPID(30))
	configuredModelPath := testPublicID(t, publicid.KindConfiguredModel, testHTTPID(31))
	modelGrantPath := testPublicID(t, publicid.KindProjectModelGrant, testHTTPID(32))
	tests := []struct {
		name    string
		method  string
		path    string
		body    string
		headers map[string]string
		want    int
	}{
		{
			name:    "create org route exact match",
			method:  http.MethodPost,
			path:    "/api/v1/orgs",
			body:    `{}`,
			headers: map[string]string{"Idempotency-Key": "route-match"},
			want:    http.StatusForbidden,
		},
		{
			name:   "create project route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "list projects route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects",
			want:   http.StatusForbidden,
		},
		{
			name:   "create agent config route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agent-configs",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "get agent config route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agent-configs/" + agentConfigPath,
			want:   http.StatusForbidden,
		},
		{
			name:   "create agent profile route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agent-profiles",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "get agent profile route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agent-profiles/" + agentProfilePath,
			want:   http.StatusForbidden,
		},
		{
			name:   "update agent profile config route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agent-profiles/" + agentProfilePath + "/config",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "rename agent profile route exact match",
			method: http.MethodPatch,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agent-profiles/" + agentProfilePath,
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "slack setup route exact match",
			method: http.MethodPost,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agent-profiles/" + agentProfilePath +
				"/slack-setup",
			body: `{}`,
			want: http.StatusForbidden,
		},
		{
			name:   "integration oauth callback route exact match",
			method: http.MethodGet,
			path:   integrationOAuthCallbackPath,
			want:   http.StatusServiceUnavailable,
		},
		{
			name:   "integration events provider route exact match",
			method: http.MethodPost,
			path:   integrationEventsPath,
			want:   http.StatusBadRequest,
		},
		{
			name:   "integration actions provider route exact match",
			method: http.MethodPost,
			path:   integrationActionsPath,
			want:   http.StatusBadRequest,
		},
		{
			name:   "create agent route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "get agent route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + agentPath,
			want:   http.StatusForbidden,
		},
		{
			name:   "update agent config route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/config",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "create input route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/inputs",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "list tool calls route exact match",
			method: http.MethodGet,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath +
				"/tool-calls",
			want: http.StatusForbidden,
		},
		{
			name:   "submit tool call result route exact match",
			method: http.MethodPost,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath +
				"/tool-calls/" + toolCallPath + "/result",
			body: `{}`,
			want: http.StatusForbidden,
		},
		{
			name:   "list turns route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/turns",
			want:   http.StatusForbidden,
		},
		{
			name:   "list turn events route exact match",
			method: http.MethodGet,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/turns/" +
				turnPath + "/events",
			want: http.StatusForbidden,
		},
		{
			name:   "list events route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/events",
			want:   http.StatusForbidden,
		},
		{
			name:   "stream events route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/events/stream",
			want:   http.StatusForbidden,
		},
		{
			name:   "cancel agent route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/cancel",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "archive agent route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/archive",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "list interactions route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/interactions",
			want:   http.StatusForbidden,
		},
		{
			name:   "resolve interaction route exact match",
			method: http.MethodPost,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/interactions/" +
				interactionPath + "/resolve",
			body: `{}`,
			want: http.StatusForbidden,
		},
		{
			name:   "list backlog route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/inputs/backlog",
			want:   http.StatusForbidden,
		},
		{
			name:   "cancel input route exact match",
			method: http.MethodPost,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/inputs/" +
				inputPath + "/cancel",
			body: `{}`,
			want: http.StatusForbidden,
		},
		{
			name:   "move input route exact match",
			method: http.MethodPost,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/inputs/" +
				inputPath + "/move",
			body: `{}`,
			want: http.StatusForbidden,
		},
		{
			name:   "promote input route exact match",
			method: http.MethodPost,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/inputs/" +
				inputPath + "/promote_to_steering",
			body: `{}`,
			want: http.StatusForbidden,
		},
		{
			name:   "demote input route exact match",
			method: http.MethodPost,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/inputs/" +
				inputPath + "/demote_to_queued",
			body: `{}`,
			want: http.StatusForbidden,
		},
		{
			name:   "artifact route exact match",
			method: http.MethodGet,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/artifacts/" +
				artifactPath,
			want: http.StatusForbidden,
		},
		{
			name:   "artifact content route exact match",
			method: http.MethodGet,
			path: "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/" + nestedAgentPath + "/artifacts/" +
				artifactPath + "/content",
			want: http.StatusForbidden,
		},
		{
			name:   "list project machines route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/machines",
			want:   http.StatusForbidden,
		},
		{
			name:   "create project machine grant route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/machine-grants",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "list project machine grant route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/machine-grants",
			want:   http.StatusForbidden,
		},
		{
			name:   "delete project machine grant route exact match",
			method: http.MethodDelete,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/machine-grants/" + grantPath,
			want:   http.StatusForbidden,
		},
		{
			name:   "create org invitation route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/invitations",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "create machine route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/machines",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "list machines route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/machines",
			want:   http.StatusForbidden,
		},
		{
			name:   "get machine route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/machines/" + machinePath,
			want:   http.StatusForbidden,
		},
		{
			name:   "delete machine route exact match",
			method: http.MethodDelete,
			path:   "/api/v1/orgs/" + orgPath + "/machines/" + machinePath,
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "create machine pool route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/machine-pools",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "list machine pools route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/machine-pools",
			want:   http.StatusForbidden,
		},
		{
			name:   "get machine pool route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/machine-pools/" + poolPath,
			want:   http.StatusForbidden,
		},
		{
			name:   "update machine pool route exact match",
			method: http.MethodPut,
			path:   "/api/v1/orgs/" + orgPath + "/machine-pools/" + poolPath,
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "delete machine pool route exact match",
			method: http.MethodDelete,
			path:   "/api/v1/orgs/" + orgPath + "/machine-pools/" + poolPath,
			want:   http.StatusForbidden,
		},
		{
			name:   "create model provider config route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/model-provider-configs",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "list model provider configs route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/model-provider-configs",
			want:   http.StatusForbidden,
		},
		{
			name:   "get model provider config route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/model-provider-configs/" + modelProviderConfigPath,
			want:   http.StatusForbidden,
		},
		{
			name:   "update model provider config route exact match",
			method: http.MethodPut,
			path:   "/api/v1/orgs/" + orgPath + "/model-provider-configs/" + modelProviderConfigPath,
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "delete model provider config route exact match",
			method: http.MethodDelete,
			path:   "/api/v1/orgs/" + orgPath + "/model-provider-configs/" + modelProviderConfigPath,
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "create configured model route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/model-provider-configs/" + modelProviderConfigPath + "/models",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "list configured models route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/model-provider-configs/" + modelProviderConfigPath + "/models",
			want:   http.StatusForbidden,
		},
		{
			name:   "update configured model route exact match",
			method: http.MethodPut,
			path: "/api/v1/orgs/" + orgPath + "/model-provider-configs/" + modelProviderConfigPath + "/models/" +
				configuredModelPath,
			body: `{}`,
			want: http.StatusForbidden,
		},
		{
			name:   "delete configured model route exact match",
			method: http.MethodDelete,
			path: "/api/v1/orgs/" + orgPath + "/model-provider-configs/" + modelProviderConfigPath + "/models/" +
				configuredModelPath,
			body: `{}`,
			want: http.StatusForbidden,
		},
		{
			name:   "create project model grant route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/model-grants",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "list project model grants route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/model-grants",
			want:   http.StatusForbidden,
		},
		{
			name:   "update project model grant route exact match",
			method: http.MethodPatch,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/model-grants/" + modelGrantPath,
			body:   `{"max_output_tokens":1024}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "delete project model grant route exact match",
			method: http.MethodDelete,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/model-grants/" + modelGrantPath,
			want:   http.StatusForbidden,
		},
		{
			name:   "create project machine pool grant route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/machine-pool-grants",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "list project machine pool grant route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/machine-pool-grants",
			want:   http.StatusForbidden,
		},
		{
			name:   "get project machine pool grant route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/machine-pool-grants/" + poolGrantPath,
			want:   http.StatusForbidden,
		},
		{
			name:   "update project machine pool grant route exact match",
			method: http.MethodPatch,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/machine-pool-grants/" + poolGrantPath,
			body:   `{"description":"updated"}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "delete project machine pool grant route exact match",
			method: http.MethodDelete,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/machine-pool-grants/" + poolGrantPath,
			want:   http.StatusForbidden,
		},
		{
			name:   "update secret route exact match",
			method: http.MethodPatch,
			path:   "/api/v1/orgs/" + orgPath + "/secrets/" + secretPath,
			body:   `{"metadata":{}}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "removed user secret metadata route",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/users/me/secrets/" + secretPath + "/metadata",
			want:   http.StatusNotFound,
		},
		{
			name:   "removed project secret metadata route",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/secrets/" + secretPath + "/metadata",
			want:   http.StatusNotFound,
		},
		{
			name:   "create daemon token route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/machines/" + machinePath + "/daemon-tokens",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "create daemon token accepts empty optional body",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/machines/" + machinePath + "/daemon-tokens",
			want:   http.StatusForbidden,
		},
		{
			name:   "list daemon token route exact match",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/machines/" + machinePath + "/daemon-tokens",
			want:   http.StatusForbidden,
		},
		{
			name:   "revoke daemon token route exact match",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/machines/" + machinePath + "/daemon-tokens/" + tokenPath + "/revoke",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "daemon runtime route exact match",
			method: http.MethodPost,
			path:   "/api/v1/daemon/runtimes",
			body:   `{}`,
			want:   http.StatusForbidden,
		},
		{
			name:   "daemon socket route exact match",
			method: http.MethodGet,
			path:   "/api/v1/daemon/runtimes/" + runtimePath + "/socket",
			want:   http.StatusForbidden,
		},
		{
			name:   "daemon end route exact match",
			method: http.MethodPost,
			path:   "/api/v1/daemon/runtimes/" + runtimePath + "/end",
			want:   http.StatusForbidden,
		},
		{
			name:   "daemon sleep route exact match",
			method: http.MethodPost,
			path:   "/api/v1/daemon/runtimes/" + runtimePath + "/sleep",
			want:   http.StatusForbidden,
		},
		{
			name:   "extra segment rejected",
			method: http.MethodPost,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents/extra",
			want:   http.StatusNotFound,
		},
		{
			name:   "wrong method rejected through json not found",
			method: http.MethodPut,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/agents",
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in project id rejected",
			method: http.MethodPost,
			path:   "/api/v1/orgs/org_test/projects/prj%2Ftest/agents",
			want:   http.StatusNotFound,
		},
		{
			name:   "project artifact route rejected",
			method: http.MethodGet,
			path:   "/api/v1/orgs/" + orgPath + "/projects/" + projectPath + "/artifacts/" + artifactPath,
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in agent config id rejected",
			method: http.MethodGet,
			path:   "/api/v1/orgs/org_test/projects/prj_test/agent-configs/agc%2Ftest",
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in leaf id rejected",
			method: http.MethodGet,
			path:   "/api/v1/orgs/org_test/projects/prj_test/agents/agt_test/artifacts/art%2Ftest",
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in org id rejected",
			method: http.MethodPost,
			path:   "/api/v1/orgs/org%2Ftest/projects/prj_test/agents",
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in machine id rejected",
			method: http.MethodGet,
			path:   "/api/v1/orgs/org_test/machines/mach%2Ftest",
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in machine pool id rejected",
			method: http.MethodGet,
			path:   "/api/v1/orgs/org_test/machine-pools/mpo%2Ftest",
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in update machine pool id rejected",
			method: http.MethodPut,
			path:   "/api/v1/orgs/org_test/machine-pools/mpo%2Ftest",
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in delete machine pool id rejected",
			method: http.MethodDelete,
			path:   "/api/v1/orgs/org_test/machine-pools/mpo%2Ftest",
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in project machine pool grant id rejected",
			method: http.MethodPost,
			path:   "/api/v1/orgs/org_test/projects/prj_test/machine-pool-grants/pmpg%2Ftest/revoke",
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in token id rejected",
			method: http.MethodPost,
			path:   "/api/v1/orgs/org_test/machines/mach_test/daemon-tokens/tok%2Ftest/revoke",
			want:   http.StatusNotFound,
		},
		{
			name:   "escaped slash in runtime id rejected",
			method: http.MethodGet,
			path:   "/api/v1/daemon/runtimes/drt%2Ftest/socket",
			want:   http.StatusNotFound,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var body io.Reader
			if tt.body != "" {
				body = strings.NewReader(tt.body)
			}
			req := httptest.NewRequest(tt.method, tt.path, body)
			for name, value := range tt.headers {
				req.Header.Set(name, value)
			}
			rec := httptest.NewRecorder()
			handler.ServeHTTP(rec, req)
			if rec.Code != tt.want {
				t.Fatalf("%s %s status=%d want=%d body=%s", tt.method, tt.path, rec.Code, tt.want, rec.Body.String())
			}
		})
	}
}

func TestSafeReturnToRejectsExternalForms(t *testing.T) {
	tests := map[string]string{
		"":                  "/",
		"login":             "/",
		"//evil.example":    "/",
		"/\\evil.example":   "/",
		"/safe/path?x=true": "/safe/path?x=true",
	}
	for input, want := range tests {
		if got := httpauth.SafeReturnTo(input); got != want {
			t.Fatalf("safeReturnTo(%q)=%q want %q", input, got, want)
		}
	}
}

func TestWriteModelOutputDeltaFrameSkipsCompletedContext(t *testing.T) {
	modelCallContextID, err := publicID(publicid.KindModelCallContext, httpTestModelContext)
	if err != nil {
		t.Fatalf("encode model context id: %v", err)
	}
	payload := json.RawMessage(
		`{"model_call_context_id":` + strconv.Quote(modelCallContextID) + `,"seq":1,"event":{"kind":"text_delta","delta":"late"}}`,
	)

	rec := httptest.NewRecorder()
	if !writeModelOutputDeltaFrame(rec, payload, map[string]struct{}{}) {
		t.Fatal("writeModelOutputDeltaFrame returned false for active context")
	}
	if body := rec.Body.String(); !strings.Contains(body, "event: model_output_delta") ||
		!strings.Contains(body, `"model_call_context_id":`) {
		t.Fatalf("active context delta was not written as SSE frame: %q", body)
	}

	rec = httptest.NewRecorder()
	if !writeModelOutputDeltaFrame(
		rec,
		payload,
		map[string]struct{}{modelCallContextID: {}},
	) {
		t.Fatal("writeModelOutputDeltaFrame returned false for completed context")
	}
	if body := rec.Body.String(); body != "" {
		t.Fatalf("completed context delta should be suppressed, got %q", body)
	}
}

func TestWriteModelOutputDeltaFrameDropsInvalidJSON(t *testing.T) {
	rec := httptest.NewRecorder()
	if !writeModelOutputDeltaFrame(rec, json.RawMessage(`{"seq":`), map[string]struct{}{}) {
		t.Fatal("writeModelOutputDeltaFrame returned false for dropped payload")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("invalid stream delta was written as SSE: %q", rec.Body.String())
	}
}

func TestWriteToolCallUpdateFrameSkipsUnknownState(t *testing.T) {
	rec := httptest.NewRecorder()
	if !writeToolCallUpdateFrame(rec, notifications.ToolCallUpdatedCommitted{
		ToolCallID: httpTestToolCallID,
		State:      "unknown",
	}) {
		t.Fatal("writeToolCallUpdateFrame returned false for unknown state")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("unknown tool call state was written as SSE: %q", rec.Body.String())
	}
}

func TestWriteSSEFrameEncodesMultilineData(t *testing.T) {
	rec := httptest.NewRecorder()
	if !writeSSEFrame(rec, "model_output_delta", "", "first\nevent: injected\r\nthird") {
		t.Fatal("writeSSEFrame returned false")
	}
	want := "event: model_output_delta\n" +
		"data: first\n" +
		"data: event: injected\n" +
		"data: third\n\n"
	if got := rec.Body.String(); got != want {
		t.Fatalf("SSE frame = %q, want %q", got, want)
	}
}

func TestWriteSSEFrameRejectsInvalidFields(t *testing.T) {
	for _, tc := range []struct {
		name      string
		eventName string
		id        string
	}{
		{name: "event newline", eventName: "model_output_delta\nevent: injected"},
		{name: "id newline", eventName: "model_output_delta", id: "1\nid: injected"},
		{name: "id null", eventName: "model_output_delta", id: "1\x00ignored"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			if writeSSEFrame(rec, tc.eventName, tc.id, `{}`) {
				t.Fatal("writeSSEFrame returned true for an invalid field")
			}
			if rec.Body.Len() != 0 {
				t.Fatalf("invalid SSE field wrote partial frame %q", rec.Body.String())
			}
		})
	}
}

func TestWriteSSEJSONFrameDoesNotWriteInvalidJSONValue(t *testing.T) {
	rec := httptest.NewRecorder()
	if writeSSEJSONFrame(rec, "error", "", make(chan int)) {
		t.Fatal("writeSSEJSONFrame returned true for an invalid JSON value")
	}
	if rec.Body.Len() != 0 {
		t.Fatalf("invalid JSON value wrote a partial SSE frame %q", rec.Body.String())
	}
}

func assertNoJSONField(t *testing.T, value any, field string) {
	t.Helper()
	body, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal value: %v", err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatalf("decode marshaled value: %v", err)
	}
	if _, ok := fields[field]; ok {
		t.Fatalf("field %q must not be present in %s", field, body)
	}
}
