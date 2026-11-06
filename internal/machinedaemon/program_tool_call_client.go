package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
	"github.com/omnara-ai/omnara/internal/publicid"
)

// Exit codes of `omnarad call`.
const (
	ProgramToolCallExitSucceeded   = 0
	ProgramToolCallExitToolFailed  = 1
	ProgramToolCallExitUnavailable = 2
	ProgramToolCallExitTransport   = 3
)

const (
	programToolCallUnavailableMessage = "omnarad call works only inside an Omnara run_command process " +
		"on a machine with programmatic tool calls enabled"
	programToolCallMaxAttempts   = 8
	programToolCallMaxBackoff    = 5 * time.Second
	programToolCallMinPollPeriod = time.Second
	// programToolCallRequestMargin covers the daemon's own forwarding margin.
	programToolCallRequestMargin = programToolCallForwardMargin + 5*time.Second
)

// ProgramToolCallOutput is what `omnarad call` prints for a completed call.
type ProgramToolCallOutput string

const (
	// ProgramToolCallOutputResult prints one JSON value: the structured
	// result, or the text as a JSON string when there is none.
	ProgramToolCallOutputResult ProgramToolCallOutput = "result"
	// ProgramToolCallOutputEnvelope prints the full result envelope, whatever
	// the outcome.
	ProgramToolCallOutputEnvelope ProgramToolCallOutput = "envelope"
	// ProgramToolCallOutputText prints the text as plain text, or the result
	// as JSON when there is no text.
	ProgramToolCallOutputText ProgramToolCallOutput = "text"
)

type ProgramToolCallOptions struct {
	Tool string
	// Input is a JSON object, "-" to read one from stdin, or empty for {}.
	Input string
	// Output defaults to ProgramToolCallOutputResult.
	Output  ProgramToolCallOutput
	Timeout time.Duration
}

// RunProgramToolCall implements `omnarad call` and returns its exit code.
func RunProgramToolCall(
	ctx context.Context,
	options ProgramToolCallOptions,
	stdin io.Reader,
	stdout, stderr io.Writer,
) int {
	fail := func(code int, format string, args ...any) int {
		_, _ = fmt.Fprintf(stderr, "omnarad call: "+format+"\n", args...)
		return code
	}
	switch options.Output {
	case "":
		options.Output = ProgramToolCallOutputResult
	case ProgramToolCallOutputResult, ProgramToolCallOutputEnvelope, ProgramToolCallOutputText:
	default:
		return fail(ProgramToolCallExitUnavailable, "--output must be result, envelope or text")
	}
	processID := os.Getenv(programToolProcessIDEnv)
	endpoint := os.Getenv(programToolEndpointEnv)
	capability := os.Getenv(programToolCapabilityEnv)
	if processID == "" || endpoint == "" || capability == "" {
		_, _ = fmt.Fprintln(stderr, programToolCallUnavailableMessage)
		return ProgramToolCallExitUnavailable
	}
	if _, err := publicid.Decode(publicid.KindProcess, processID); err != nil {
		return fail(ProgramToolCallExitUnavailable, "%s is not a valid process ID", programToolProcessIDEnv)
	}
	if strings.TrimSpace(options.Tool) == "" {
		return fail(ProgramToolCallExitUnavailable, "tool name is required")
	}
	if options.Timeout <= 0 {
		return fail(ProgramToolCallExitUnavailable, "--timeout must be positive")
	}
	input, err := readProgramToolCallInput(options.Input, stdin)
	if err != nil {
		return fail(ProgramToolCallExitUnavailable, "%v", err)
	}
	ctx, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	caller := newProgramToolCaller(endpoint, processID, capability)
	defer caller.closeIdleConnections()
	request, err := json.Marshal(daemonprotocol.ProgramToolCallRequest{
		Key: uuid.NewString(), Name: options.Tool, Input: input,
	})
	if err != nil {
		return fail(ProgramToolCallExitUnavailable, "cannot encode tool call request")
	}
	result, err := caller.submit(ctx, request)
	announced := false
	for err == nil && result.State != daemonprotocol.ProgramToolCallStateCompleted {
		if result.ToolCallID == "" {
			return fail(ProgramToolCallExitTransport, "daemon returned an incomplete tool call without an ID")
		}
		if result.State == daemonprotocol.ProgramToolCallStateAwaitingApproval && !announced {
			announced = true
			_, _ = fmt.Fprintf(stderr, "omnarad: waiting for approval of %s in Omnara...\n", options.Tool)
		}
		result, err = caller.wait(ctx, result)
	}
	if err != nil {
		return reportProgramToolCallError(ctx, options.Tool, result, err, stderr)
	}
	return writeProgramToolCallResult(result, options.Output, stdout, stderr)
}

func readProgramToolCallInput(raw string, stdin io.Reader) (json.RawMessage, error) {
	data := []byte(raw)
	switch raw {
	case "":
		data = []byte("{}")
	case "-":
		if stdin == nil {
			return nil, errors.New("input is not available on stdin")
		}
		var err error
		data, err = io.ReadAll(io.LimitReader(stdin, programToolCallMaxRequestBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read input from stdin: %w", err)
		}
	}
	if len(data) > programToolCallMaxRequestBytes {
		return nil, errors.New("input exceeds 1 MiB")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	var object map[string]json.RawMessage
	if err := decoder.Decode(&object); err != nil || object == nil {
		return nil, errors.New("input must be a JSON object")
	}
	if _, err := decoder.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("input must be a single JSON object")
	}
	var compact bytes.Buffer
	if err := json.Compact(&compact, data); err != nil {
		return nil, errors.New("input must be a JSON object")
	}
	return compact.Bytes(), nil
}

type programToolCallStatusError struct {
	StatusCode int
	Message    string
	RetryAfter string
}

func (e programToolCallStatusError) Error() string { return e.Message }

type programToolCaller struct {
	processID  string
	capability string
	transport  *http.Transport
	client     *http.Client
}

func newProgramToolCaller(endpoint, processID, capability string) *programToolCaller {
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			for {
				conn, err := localipc.Dial(connectCtx, endpoint)
				if err == nil {
					return conn, nil
				}
				if err := sleepContext(connectCtx, 250*time.Millisecond); err != nil {
					return nil, errors.New("daemon is unavailable for tool calls")
				}
			}
		},
	}
	return &programToolCaller{
		processID:  processID,
		capability: capability,
		transport:  transport,
		client: &http.Client{
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (c *programToolCaller) closeIdleConnections() { c.transport.CloseIdleConnections() }

func (c *programToolCaller) submit(ctx context.Context, body []byte) (daemonprotocol.ProgramToolCallResult, error) {
	return c.retry(ctx, func() (daemonprotocol.ProgramToolCallResult, error) {
		return c.do(ctx, http.MethodPost, "/tool-calls/"+c.processID, body,
			programToolCallSubmitTimeout+programToolCallRequestMargin)
	})
}

// wait long-polls until the call changes state or the poll window ends.
func (c *programToolCaller) wait(
	ctx context.Context,
	current daemonprotocol.ProgramToolCallResult,
) (daemonprotocol.ProgramToolCallResult, error) {
	started := time.Now()
	result, err := c.retry(ctx, func() (daemonprotocol.ProgramToolCallResult, error) {
		waitMs := daemonprotocol.ProgramToolCallMaxWaitMs
		if deadline, ok := ctx.Deadline(); ok {
			waitMs = min(waitMs, max(0, int(time.Until(deadline).Milliseconds())))
		}
		path := "/tool-calls/" + c.processID + "/" + url.PathEscape(current.ToolCallID) +
			"?wait_ms=" + strconv.Itoa(waitMs)
		return c.do(ctx, http.MethodGet, path, nil,
			time.Duration(waitMs)*time.Millisecond+programToolCallRequestMargin)
	})
	if err != nil {
		return current, err
	}
	if result.ToolCallID == "" {
		result.ToolCallID = current.ToolCallID
	}
	// Guard against a backend that answers polls immediately without a change.
	if result.State == current.State && time.Since(started) < programToolCallMinPollPeriod {
		if err := sleepContext(ctx, programToolCallMinPollPeriod-time.Since(started)); err != nil {
			return result, err
		}
	}
	return result, nil
}

func (c *programToolCaller) retry(
	ctx context.Context,
	attempt func() (daemonprotocol.ProgramToolCallResult, error),
) (daemonprotocol.ProgramToolCallResult, error) {
	backoff := 250 * time.Millisecond
	for try := 1; ; try++ {
		result, err := attempt()
		if err == nil || ctx.Err() != nil || try >= programToolCallMaxAttempts {
			return result, err
		}
		delay := backoff
		backoff = min(programToolCallMaxBackoff, 2*backoff)
		var status programToolCallStatusError
		if errors.As(err, &status) {
			if status.StatusCode != http.StatusTooManyRequests && status.StatusCode != http.StatusServiceUnavailable {
				return result, err
			}
			if retryAfter, ok := parseRetryAfter(status.RetryAfter, time.Now()); ok {
				delay = max(delay, retryAfter)
			}
		}
		if err := sleepContext(ctx, delay); err != nil {
			return result, err
		}
	}
}

func (c *programToolCaller) do(
	ctx context.Context,
	method, path string,
	body []byte,
	timeout time.Duration,
) (daemonprotocol.ProgramToolCallResult, error) {
	var result daemonprotocol.ProgramToolCallResult
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	var requestBody io.Reader
	if body != nil {
		requestBody = bytes.NewReader(body)
	}
	request, err := http.NewRequestWithContext(ctx, method, "http://daemon"+path, requestBody)
	if err != nil {
		return result, errors.New("cannot prepare tool call request")
	}
	request.Header.Set("Authorization", "Bearer "+c.capability)
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	response, err := c.client.Do(request)
	if err != nil {
		return result, errors.New("daemon could not be reached for tool calls")
	}
	defer func() { _ = response.Body.Close() }()
	data, err := io.ReadAll(io.LimitReader(response.Body, programToolCallMaxResponseBytes+1))
	if err != nil {
		return result, errors.New("tool call response could not be read")
	}
	if len(data) > programToolCallMaxResponseBytes {
		return result, errors.New("tool call response exceeds 16 MiB")
	}
	if response.StatusCode != http.StatusOK {
		return result, programToolCallStatusError{
			StatusCode: response.StatusCode,
			Message:    programToolCallErrorMessage(response, data),
			RetryAfter: response.Header.Get("Retry-After"),
		}
	}
	if err := json.Unmarshal(data, &result); err != nil || result.State == "" {
		return daemonprotocol.ProgramToolCallResult{}, errors.New("invalid tool call response")
	}
	return result, nil
}

func programToolCallErrorMessage(response *http.Response, body []byte) string {
	var apiError struct {
		Error  string          `json:"error"`
		Code   string          `json:"code"`
		Issues json.RawMessage `json:"issues"`
	}
	if err := json.Unmarshal(body, &apiError); err != nil || apiError.Error == "" {
		return response.Status
	}
	message := apiError.Error
	if apiError.Code != "" {
		message += " (" + apiError.Code + ")"
	}
	if len(apiError.Issues) > 0 && string(apiError.Issues) != "null" {
		message += ": " + string(apiError.Issues)
	}
	return message
}

func reportProgramToolCallError(
	ctx context.Context,
	tool string,
	last daemonprotocol.ProgramToolCallResult,
	err error,
	stderr io.Writer,
) int {
	var status programToolCallStatusError
	switch {
	case errors.As(err, &status):
		_, _ = fmt.Fprintf(stderr, "omnarad call: %s: %s\n", tool, status.Message)
		switch status.StatusCode {
		case http.StatusBadRequest, http.StatusForbidden, http.StatusNotFound, http.StatusGone,
			http.StatusRequestEntityTooLarge, http.StatusUnprocessableEntity:
			return ProgramToolCallExitUnavailable
		}
		return ProgramToolCallExitTransport
	case errors.Is(ctx.Err(), context.DeadlineExceeded):
		if last.ToolCallID != "" {
			_, _ = fmt.Fprintf(stderr, "omnarad call: %s: timed out; tool call %s may still complete\n",
				tool, last.ToolCallID)
		} else {
			_, _ = fmt.Fprintf(stderr, "omnarad call: %s: timed out\n", tool)
		}
	case ctx.Err() != nil:
		_, _ = fmt.Fprintf(stderr, "omnarad call: %s: canceled\n", tool)
	default:
		_, _ = fmt.Fprintf(stderr, "omnarad call: %s: %v\n", tool, err)
	}
	return ProgramToolCallExitTransport
}

func writeProgramToolCallResult(
	result daemonprotocol.ProgramToolCallResult,
	output ProgramToolCallOutput,
	stdout, stderr io.Writer,
) int {
	code := ProgramToolCallExitSucceeded
	if result.Outcome != daemonprotocol.ProgramToolCallOutcomeSucceeded {
		code = ProgramToolCallExitToolFailed
	}
	if output == ProgramToolCallOutputEnvelope {
		if err := writeJSONLine(stdout, result); err != nil {
			return ProgramToolCallExitTransport
		}
		return code
	}
	if code != ProgramToolCallExitSucceeded {
		content := result.Content
		if content == nil {
			content = []daemonprotocol.ProgramToolCallContent{}
		}
		_ = writeJSONLine(stderr, struct {
			Outcome string                                  `json:"outcome"`
			Content []daemonprotocol.ProgramToolCallContent `json:"content"`
		}{result.Outcome, content})
		return code
	}
	var texts []string
	for _, part := range result.Content {
		if part.Type == daemonprotocol.ProgramToolCallContentText {
			texts = append(texts, part.Text)
		}
	}
	text := strings.Join(texts, "\n")
	if output == ProgramToolCallOutputText && len(texts) > 0 {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if _, err := io.WriteString(stdout, text); err != nil {
			return ProgramToolCallExitTransport
		}
		return code
	}
	// Every success prints exactly one JSON value, so commands can always
	// parse stdout.
	var value any
	switch {
	case len(result.Structured) > 0 && string(result.Structured) != "null":
		var compact bytes.Buffer
		if err := json.Compact(&compact, result.Structured); err != nil {
			_, _ = fmt.Fprintln(stderr, "omnarad call: invalid structured tool result")
			return ProgramToolCallExitTransport
		}
		value = json.RawMessage(compact.Bytes())
	case len(texts) > 0:
		value = text
	case len(result.Content) > 0:
		value = result.Content
	}
	if err := writeJSONLine(stdout, value); err != nil {
		return ProgramToolCallExitTransport
	}
	return code
}

func writeJSONLine(w io.Writer, value any) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	_, err = w.Write(append(data, '\n'))
	return err
}
