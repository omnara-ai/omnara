package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/omnara-ai/omnara/internal/daemonprotocol"
	"github.com/omnara-ai/omnara/internal/publicid"
)

const (
	ProgramToolCallExitSucceeded   = 0
	ProgramToolCallExitToolFailed  = 1
	ProgramToolCallExitUnavailable = 2
	ProgramToolCallExitTransport   = 3
)

const (
	programToolCallUnavailableMessage = "omnarad call works only inside an Omnara run_command process " +
		"on a machine with programmatic tool calls enabled"
	programToolCallMaxAttempts = 8
	programToolCallMaxBackoff  = 5 * time.Second
)

type ProgramToolCallOutput string

const (
	ProgramToolCallOutputResult   ProgramToolCallOutput = "result"
	ProgramToolCallOutputEnvelope ProgramToolCallOutput = "envelope"
	ProgramToolCallOutputText     ProgramToolCallOutput = "text"
)

type ProgramToolCallOptions struct {
	Tool    string
	Input   string
	Output  ProgramToolCallOutput
	Timeout time.Duration
}

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
	if processID == "" || endpoint == "" {
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
	caller := newProgramToolCaller(endpoint, processID)
	defer caller.client.CloseIdleConnections()
	request, err := json.Marshal(daemonprotocol.ProgramToolCallRequest{
		Key: uuid.NewString(), Name: options.Tool, Input: input,
	})
	if err != nil {
		return fail(ProgramToolCallExitUnavailable, "cannot encode tool call request")
	}
	result, err := caller.call(ctx, request, func() {
		_, _ = fmt.Fprintf(stderr, "omnarad: waiting for approval of %s in Omnara...\n", options.Tool)
	})
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
		data, err = io.ReadAll(io.LimitReader(stdin, daemonprotocol.ProgramToolCallMaxInputBytes+1))
		if err != nil {
			return nil, fmt.Errorf("read input from stdin: %w", err)
		}
	}
	if len(data) > daemonprotocol.ProgramToolCallMaxInputBytes {
		return nil, errors.New("input is too large")
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

type programToolCaller struct {
	processID string
	client    *http.Client
}

func newProgramToolCaller(endpoint, processID string) *programToolCaller {
	return &programToolCaller{processID: processID, client: &http.Client{
		Transport:     dialProcessService(endpoint, "daemon is unavailable for tool calls"),
		CheckRedirect: noRedirects}}
}

func (c *programToolCaller) call(
	ctx context.Context, body []byte, awaitingApproval func(),
) (daemonprotocol.ProgramToolCallResult, error) {
	var last daemonprotocol.ProgramToolCallResult
	backoff := 250 * time.Millisecond
	for try := 1; ; try++ {
		err := c.attempt(ctx, body, func(result daemonprotocol.ProgramToolCallResult) {
			if result.State == daemonprotocol.ProgramToolCallStateAwaitingApproval && last.State != result.State {
				awaitingApproval()
			}
			last = result
		})
		if err == nil || ctx.Err() != nil || try >= programToolCallMaxAttempts {
			return last, err
		}
		delay := backoff
		backoff = min(programToolCallMaxBackoff, 2*backoff)
		var status httpStatusError
		if errors.As(err, &status) {
			if status.StatusCode != http.StatusTooManyRequests && status.StatusCode != http.StatusServiceUnavailable {
				return last, err
			}
			if retryAfter, ok := retryAfterDelay(err, time.Now()); ok {
				delay = max(delay, retryAfter)
			}
		}
		if err := sleepContext(ctx, delay); err != nil {
			return last, err
		}
	}
}

func (c *programToolCaller) attempt(
	ctx context.Context, body []byte, seen func(daemonprotocol.ProgramToolCallResult),
) error {
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, "http://daemon/tool-calls/"+c.processID,
		bytes.NewReader(body))
	if err != nil {
		return errors.New("cannot prepare tool call request")
	}
	request.Header.Set("Content-Type", "application/json")
	response, err := c.client.Do(request)
	if err != nil {
		return errors.New("daemon could not be reached for tool calls")
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(response.Body, daemonprotocol.MaxMessageBytes))
		return httpStatusError{
			StatusCode: response.StatusCode,
			Status:     response.Status,
			Body:       programToolCallErrorMessage(response, data),
			RetryAfter: response.Header.Get("Retry-After"),
		}
	}
	reports := json.NewDecoder(response.Body)
	for {
		var report daemonprotocol.ToolCallReport
		if err := reports.Decode(&report); err != nil {
			return errors.New("daemon stopped before the tool call completed")
		}
		switch {
		case report.AckStatus == daemonprotocol.AckStatusTransientError:
			return httpStatusError{StatusCode: http.StatusServiceUnavailable,
				Body: report.Error + " (" + report.ErrorCode + ")"}
		case report.AckStatus != "":
			return httpStatusError{StatusCode: http.StatusUnprocessableEntity,
				Body: report.Error + " (" + report.ErrorCode + ")"}
		}
		seen(report.ProgramToolCallResult)
		if report.State == daemonprotocol.ProgramToolCallStateCompleted {
			return nil
		}
	}
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
	var status httpStatusError
	switch {
	case errors.As(err, &status):
		_, _ = fmt.Fprintf(stderr, "omnarad call: %s: %s\n", tool, status.Body)
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
	var structured json.RawMessage
	for _, part := range result.Content {
		switch {
		case part.Type == daemonprotocol.ProgramToolCallContentText:
			texts = append(texts, part.Text)
		case part.Type == daemonprotocol.ProgramToolCallContentStructuredData && structured == nil:
			structured = part.Value
		}
	}
	text := strings.Join(texts, "\n")
	var value any
	switch {
	case len(structured) > 0 && string(structured) != "null":
		var compact bytes.Buffer
		if err := json.Compact(&compact, structured); err != nil {
			_, _ = fmt.Fprintln(stderr, "omnarad call: invalid structured tool result")
			return ProgramToolCallExitTransport
		}
		value = json.RawMessage(compact.Bytes())
	case len(texts) > 0:
		value = text
	case len(result.Content) > 0:
		value = result.Content
	}
	if _, isText := value.(string); isText && output == ProgramToolCallOutputText {
		if !strings.HasSuffix(text, "\n") {
			text += "\n"
		}
		if _, err := io.WriteString(stdout, text); err != nil {
			return ProgramToolCallExitTransport
		}
		return code
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
