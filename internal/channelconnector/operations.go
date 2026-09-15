package channelconnector

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/omnara-ai/omnara/internal/jsoncanonical"
)

const (
	MaxOperationArtifacts           = 20
	MaxOperationEnvelopeBytes       = 512 * 1024
	MaxOperationResponseBytes int64 = 1024 * 1024
)

type OperationKind string

const (
	OperationSend        OperationKind = "send"
	OperationRead        OperationKind = "read"
	OperationInteraction OperationKind = "interaction"
)

type OperationOutcome string

const (
	OperationCompleted OperationOutcome = "completed"
	OperationFailed    OperationOutcome = "failed"
	OperationUnknown   OperationOutcome = "unknown"
)

// OperationScope is resolved by core after live authorization. These references
// are correlation and scope assertions, never authority supplied by the model.
type OperationScope struct {
	ProjectID            string `json:"project_id"`
	IntegrationAppID     string `json:"integration_app_id"`
	IntegrationInstallID string `json:"integration_install_id"`
	AgentID              string `json:"agent_id"`
	ChannelID            string `json:"channel_id"`
}

// OperationRequest is a transport envelope, not a model tool declaration.
// Payload must be produced from the authorized operation's typed arguments.
// Send/read/interaction payloads and completion mapping remain the caller's job.
type OperationRequest struct {
	RequestID  string
	Capability Capability
	Kind       OperationKind
	Scope      OperationScope
	Payload    json.RawMessage
	Artifacts  []OperationArtifact
}

// OperationArtifact opens an already-authorized artifact lazily. Open must obey
// ctx, and Close must unblock Read, including when called concurrently. Execute
// owns and closes each returned reader. No source URL crosses the wire.
type OperationArtifact struct {
	ID          string
	Filename    string
	ContentType string
	Open        func(ctx context.Context) (io.ReadCloser, error)
}

// OperationResult is the gateway's correlated terminal response. A completed
// payload describes publication (including draft), history, or presentation.
// Core must validate it and register any continuation before exposing results.
// Failed asserts no publication occurred; unknown must never imply safe resend.
type OperationResult struct {
	RequestID string           `json:"request_id"`
	Outcome   OperationOutcome `json:"outcome"`
	Payload   json.RawMessage  `json:"payload,omitempty"`
}

// OperationError contains only local, fixed diagnostics. We deliberately do not
// wrap HTTP/source errors or echo gateway bodies, URLs, or credentials.
type OperationError struct {
	Outcome    OperationOutcome
	Code       string
	StatusCode int
	cause      error
}

func (e *OperationError) Error() string {
	return "channel operation " + string(e.Outcome) + ": " + e.Code
}

func (e *OperationError) Unwrap() error { return e.cause }

type operationRoute struct {
	url   string
	token string
}

type OperationsClient struct {
	http   *http.Client
	routes map[Capability]operationRoute
}

// NewOperationsClient snapshots deployment routes. Every outbound capability
// pair must occur exactly once, even within one connector. Inbound-only configs
// may omit OperationsURL; they never become fallback outbound routes. The HTTP
// client is copied with redirects, cookies, and its independent timeout disabled.
// Custom transports must honor context cancellation and must not retry requests.
func NewOperationsClient(configs []Config, client *http.Client) (*OperationsClient, error) {
	if _, err := NewAuthenticator(configs); err != nil {
		return nil, err
	}
	if client == nil {
		client = http.DefaultClient
	}
	copyClient := *client
	copyClient.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	copyClient.Jar = nil
	copyClient.Timeout = 0 // Execute requires one caller-owned deadline.
	routes := make(map[Capability]operationRoute)
	for _, config := range configs {
		normalized, err := normalizeConfig(config)
		if err != nil {
			return nil, err
		}
		if normalized.OperationsURL == "" {
			continue
		}
		if len(normalized.Capabilities) != len(config.Capabilities) {
			return nil, errors.New("outbound connector has duplicate capabilities")
		}
		for _, capability := range normalized.Capabilities {
			if _, exists := routes[capability]; exists {
				return nil, errors.New("outbound connector capability route is ambiguous")
			}
			routes[capability] = operationRoute{url: normalized.OperationsURL, token: normalized.Token}
		}
	}
	return &OperationsClient{http: &copyClient, routes: routes}, nil
}

func normalizeOperationsURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	invalid := errors.New("operations_url must be an absolute http(s) endpoint " +
		"without credentials, query, fragment, or ambiguous path")
	u, err := url.Parse(value)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" ||
		u.User != nil || u.Opaque != "" || u.RawQuery != "" || u.ForceQuery || strings.Contains(value, "#") ||
		strings.Contains(u.EscapedPath(), "%") || strings.ContainsAny(u.Path, "\\\r\n\t") {
		return "", invalid
	}
	if port := u.Port(); port != "" {
		number, err := strconv.Atoi(port)
		if err != nil || number < 1 || number > 65535 {
			return "", invalid
		}
	} else if strings.HasSuffix(u.Host, ":") {
		return "", invalid
	}
	if u.Path != "" && path.Clean(u.Path) != strings.TrimSuffix(u.Path, "/") && u.Path != "/" {
		return "", invalid
	}
	u.Host = strings.ToLower(u.Host)
	// Preserve the exact endpoint path, including a deliberate trailing slash.
	return u.String(), nil
}

type operationEnvelope struct {
	RequestID  string                      `json:"request_id"`
	Capability Capability                  `json:"capability"`
	Kind       OperationKind               `json:"kind"`
	Scope      OperationScope              `json:"scope"`
	Deadline   time.Time                   `json:"deadline"`
	Payload    json.RawMessage             `json:"payload"`
	Artifacts  []operationArtifactMetadata `json:"artifacts,omitempty"`
}

type operationArtifactMetadata struct {
	ID          string `json:"id"`
	Filename    string `json:"filename"`
	ContentType string `json:"content_type"`
}

// Execute performs exactly one POST to the configured full endpoint. ctx must
// have a deadline; the same absolute deadline covers upload and response and is
// carried in the JSON envelope for the gateway's provider retry budget. Requests
// with artifacts use multipart/form-data: first "operation" (JSON), then one
// "artifact" part per metadata entry in order. The gateway MUST finish validating
// the entire body before any provider mutation. No idempotency/replay header or
// GetBody is set: neither core nor the standard HTTP transport retries this POST.
// After dispatch, untrusted/nonterminal responses and transport errors report
// unknown for mutations (failed for reads). Only a correlated terminal envelope
// can establish a known mutation failure. The returned error is non-nil for both
// failed and unknown results; no queue or pending response exists.
func (c *OperationsClient) Execute(ctx context.Context, request OperationRequest) (OperationResult, error) {
	fail := func(outcome OperationOutcome, code string, status int) (OperationResult, error) {
		var cause error
		if ctx.Err() != nil {
			cause = ctx.Err() // Only safe, standard context sentinels are unwrapped.
		}
		return OperationResult{RequestID: request.RequestID, Outcome: outcome}, &OperationError{
			Outcome: outcome, Code: code, StatusCode: status, cause: cause,
		}
	}
	deadline, bounded := ctx.Deadline()
	if !bounded {
		return fail(OperationFailed, "deadline_required", 0)
	}
	if ctx.Err() != nil || !deadline.After(time.Now()) {
		return fail(OperationFailed, "canceled_before_dispatch", 0)
	}
	if c == nil {
		return fail(OperationFailed, "route_unavailable", 0)
	}
	route, ok := c.routes[request.Capability] // Exact pair: no provider/key fallback.
	if !ok {
		return fail(OperationFailed, "route_unavailable", 0)
	}
	envelope, err := prepareOperation(request, deadline)
	if err != nil {
		return fail(OperationFailed, "invalid_request", 0)
	}
	operationCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	body, contentType, finish := operationBody(operationCtx, envelope, request.Artifacts)
	defer func() {
		cancel()
		_ = body.Close()
		_ = finish()
	}()
	httpRequest, err := http.NewRequestWithContext(operationCtx, http.MethodPost, route.url, body)
	if err != nil {
		return fail(OperationFailed, "invalid_request", 0)
	}
	httpRequest.Header.Set("Authorization", "Bearer "+route.token)
	httpRequest.Header.Set("Content-Type", contentType)
	httpRequest.Header.Set("Accept", "application/json")
	uncertain := OperationUnknown
	if request.Kind == OperationRead {
		uncertain = OperationFailed
	}
	response, err := c.http.Do(httpRequest)
	if err != nil {
		return fail(uncertain, "transport_failed", 0)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		return fail(uncertain, "http_rejected", response.StatusCode)
	}
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	if err != nil || mediaType != "application/json" {
		return fail(uncertain, "invalid_response", response.StatusCode)
	}
	raw, err := io.ReadAll(io.LimitReader(response.Body, MaxOperationResponseBytes+1))
	if err != nil || int64(len(raw)) > MaxOperationResponseBytes {
		return fail(uncertain, "invalid_response", response.StatusCode)
	}
	// Reject a gateway claiming success without consuming the full artifact body.
	// Cancellation also closes any active source reader before returning.
	if err := finish(); err != nil {
		return fail(uncertain, "upload_failed", response.StatusCode)
	}
	if operationCtx.Err() != nil {
		return fail(uncertain, "transport_failed", response.StatusCode)
	}
	var result OperationResult
	object, err := jsoncanonical.ParseObject(raw, int(MaxOperationResponseBytes))
	if err != nil {
		return fail(uncertain, "invalid_response", response.StatusCode)
	}
	for key := range object {
		// encoding/json accepts case-insensitive struct keys; the wire contract
		// does not. Reject aliases before they can overwrite an outcome.
		if key != "request_id" && key != "outcome" && key != "payload" {
			return fail(uncertain, "invalid_response", response.StatusCode)
		}
	}
	if json.Unmarshal(raw, &result) != nil || result.RequestID != request.RequestID {
		return fail(uncertain, "invalid_response", response.StatusCode)
	}
	if operationCtx.Err() != nil || !deadline.After(time.Now()) {
		return fail(uncertain, "transport_failed", response.StatusCode)
	}
	switch result.Outcome {
	case OperationCompleted:
		if !isJSONObject(result.Payload) {
			return fail(uncertain, "invalid_response", response.StatusCode)
		}
		return result, nil
	case OperationFailed, OperationUnknown:
		// Do not surface provider-controlled error text or payload as diagnostics.
		return fail(result.Outcome, "gateway_"+string(result.Outcome), response.StatusCode)
	default:
		return fail(uncertain, "invalid_response", response.StatusCode)
	}
}

func prepareOperation(request OperationRequest, deadline time.Time) ([]byte, error) {
	invalid := errors.New("invalid operation")
	if request.Kind != OperationSend && request.Kind != OperationRead && request.Kind != OperationInteraction {
		return nil, invalid
	}
	for _, id := range []string{request.RequestID, request.Scope.ProjectID, request.Scope.IntegrationAppID,
		request.Scope.IntegrationInstallID, request.Scope.AgentID, request.Scope.ChannelID} {
		if !validOperationText(id, 256) {
			return nil, invalid
		}
	}
	if _, err := jsoncanonical.ParseObject(request.Payload, MaxMetadataBytes); err != nil {
		return nil, invalid
	}
	if len(request.Artifacts) > MaxOperationArtifacts || (request.Kind == OperationRead && len(request.Artifacts) != 0) {
		return nil, invalid
	}
	envelope := operationEnvelope{RequestID: request.RequestID, Capability: request.Capability,
		Kind: request.Kind, Scope: request.Scope, Deadline: deadline.UTC(), Payload: request.Payload}
	seen := make(map[string]bool, len(request.Artifacts))
	for _, artifact := range request.Artifacts {
		if !validOperationText(artifact.ID, 256) || seen[artifact.ID] || !validOperationText(artifact.Filename, 255) ||
			strings.ContainsAny(artifact.Filename, "/\\") || artifact.Open == nil ||
			!validOperationText(artifact.ContentType, 255) {
			return nil, invalid
		}
		mediaType, params, err := mime.ParseMediaType(artifact.ContentType)
		if err != nil || len(params) != 0 || !strings.Contains(mediaType, "/") {
			return nil, invalid
		}
		seen[artifact.ID] = true
		envelope.Artifacts = append(envelope.Artifacts, operationArtifactMetadata{
			ID: artifact.ID, Filename: artifact.Filename, ContentType: mediaType,
		})
	}
	raw, err := json.Marshal(envelope)
	if err != nil || len(raw) > MaxOperationEnvelopeBytes {
		return nil, invalid
	}
	return raw, nil
}

func validOperationText(value string, maxBytes int) bool {
	return value != "" && utf8.ValidString(value) && len(value) <= maxBytes && strings.TrimSpace(value) == value &&
		!strings.ContainsAny(value, "\x00\r\n")
}

func isJSONObject(raw []byte) bool {
	raw = bytes.TrimSpace(raw)
	return len(raw) >= 2 && raw[0] == '{' && raw[len(raw)-1] == '}'
}

func operationBody(
	ctx context.Context, envelope []byte, artifacts []OperationArtifact,
) (io.ReadCloser, string, func() error) {
	if len(artifacts) == 0 {
		// Wrap the reader to keep net/http from synthesizing a replayable GetBody.
		return io.NopCloser(bytes.NewReader(envelope)), "application/json", func() error { return nil }
	}
	reader, writer := io.Pipe()
	multipartWriter := multipart.NewWriter(writer)
	done := make(chan struct{})
	var writeErr error
	stop := context.AfterFunc(ctx, func() {
		_ = reader.CloseWithError(ctx.Err())
		_ = writer.CloseWithError(ctx.Err())
	})
	go func() {
		defer close(done)
		defer stop()
		writeErr = writeOperationMultipart(ctx, multipartWriter, envelope, artifacts)
		if writeErr == nil {
			writeErr = multipartWriter.Close()
		}
		_ = writer.CloseWithError(writeErr)
	}()
	return reader, multipartWriter.FormDataContentType(), func() error { <-done; return writeErr }
}

func writeOperationMultipart(
	ctx context.Context, writer *multipart.Writer, envelope []byte, artifacts []OperationArtifact,
) error {
	header := make(textproto.MIMEHeader)
	header.Set("Content-Disposition", `form-data; name="operation"`)
	header.Set("Content-Type", "application/json")
	part, err := writer.CreatePart(header)
	if err != nil {
		return err
	}
	if _, err := part.Write(envelope); err != nil {
		return err
	}
	for _, artifact := range artifacts {
		if err := ctx.Err(); err != nil {
			return err
		}
		header := make(textproto.MIMEHeader)
		header.Set("Content-Disposition", mime.FormatMediaType("form-data", map[string]string{
			"name": "artifact", "filename": artifact.Filename,
		}))
		header.Set("Content-Type", artifact.ContentType)
		part, err := writer.CreatePart(header)
		if err != nil {
			return err
		}
		if err := copyOperationArtifact(ctx, part, artifact); err != nil {
			return err
		}
	}
	return nil
}

func copyOperationArtifact(ctx context.Context, target io.Writer, artifact OperationArtifact) error {
	source, err := artifact.Open(ctx)
	if err != nil {
		return err
	}
	if source == nil {
		return errors.New("artifact source missing")
	}
	// Close interrupts a blocked source read, not just the HTTP pipe write.
	closed := make(chan struct{})
	stop := context.AfterFunc(ctx, func() { _ = source.Close(); close(closed) })
	defer func() {
		if stop() {
			_ = source.Close()
		} else {
			<-closed
		}
	}()
	// Stored artifacts have no universal egress byte ceiling. Stream under the
	// request deadline; the receiver meters actual request/disk bytes against
	// deployment budgets. Ingress upload limits are not egress product policy.
	if _, err := io.Copy(target, source); err != nil {
		return err
	}
	return ctx.Err()
}
