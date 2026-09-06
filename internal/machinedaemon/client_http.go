package machinedaemon

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/daemonprotocol"
)

const daemonAPIPath = "/daemon"

type httpStatusError struct {
	StatusCode int
	Status     string
	Body       string
	RetryAfter string
}

func (e httpStatusError) Error() string { return e.Status + ": " + e.Body }

func IsAuthenticationRejected(err error) bool {
	var statusErr httpStatusError
	return errors.As(err, &statusErr) &&
		(statusErr.StatusCode == http.StatusUnauthorized || statusErr.StatusCode == http.StatusForbidden)
}

func isTerminalDaemonRequestError(err error) bool {
	if IsAuthenticationRejected(err) {
		return true
	}
	var statusErr httpStatusError
	return errors.As(err, &statusErr) && statusErr.StatusCode == http.StatusBadRequest
}

var errDaemonTransportUnavailable = errors.New("daemon websocket is not connected")
var errDaemonHTTPResponseTooLarge = errors.New("daemon HTTP response exceeds the byte limit")

func sleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (c *Client) postJSON(ctx context.Context, path string, body any, out any) error {
	var requestBody io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return err
		}
		requestBody = bytes.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.cfg.APIURL+path, requestBody)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.MachineToken)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	data, readErr := readDaemonHTTPResponse(resp.Body)
	closeErr := resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.Join(
			newHTTPStatusError(resp, data),
			readErr,
			closeErr,
		)
	}
	if readErr != nil {
		return errors.Join(readErr, closeErr)
	}
	if closeErr != nil {
		return closeErr
	}
	if out != nil && len(data) > 0 {
		return json.Unmarshal(data, out)
	}
	return nil
}

func readDaemonHTTPResponse(body io.Reader) ([]byte, error) {
	data, err := io.ReadAll(io.LimitReader(body, daemonprotocol.MaxMessageBytes+1))
	if len(data) > daemonprotocol.MaxMessageBytes {
		return data[:daemonprotocol.MaxMessageBytes], errors.Join(errDaemonHTTPResponseTooLarge, err)
	}
	return data, err
}

func newHTTPStatusError(response *http.Response, body []byte) httpStatusError {
	return httpStatusError{
		StatusCode: response.StatusCode,
		Status:     response.Status,
		Body:       strings.TrimSpace(string(body)),
		RetryAfter: response.Header.Get("Retry-After"),
	}
}

func retryAfterDelay(err error, now time.Time) (time.Duration, bool) {
	var statusErr httpStatusError
	if !errors.As(err, &statusErr) {
		return 0, false
	}
	return parseRetryAfter(statusErr.RetryAfter, now)
}

func parseRetryAfter(value string, now time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := strconv.ParseUint(value, 10, 64); err == nil {
		if seconds > uint64(maximumRetryDelay/time.Second) {
			return 0, false
		}
		return time.Duration(seconds) * time.Second, true
	}
	retryAt, err := http.ParseTime(value)
	if err != nil {
		return 0, false
	}
	delay := retryAt.Sub(now)
	if delay < 0 {
		delay = 0
	}
	return delay, true
}

func marshalJSON(value any) (json.RawMessage, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	return data, nil
}
