package route

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

var ErrProviderResponseTooLarge = errors.New("model provider response exceeds the byte limit")

const ServerSentEventsMediaType = "text/event-stream"

type SSEEvent struct {
	Event string
	Data  string
}

func ReadSSEEvents(ctx context.Context, r io.Reader, onEvent func(SSEEvent) error) error {
	reader := bufio.NewReader(r)
	var (
		eventName string
		dataLines []string
	)
	flush := func() error {
		if len(dataLines) == 0 {
			eventName = ""
			return nil
		}
		ev := SSEEvent{Event: eventName, Data: strings.Join(dataLines, "\n")}
		eventName = ""
		dataLines = dataLines[:0]
		return onEvent(ev)
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		line, err := reader.ReadString('\n')
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return err
		}
		line = strings.TrimSuffix(line, "\n")
		line = strings.TrimSuffix(line, "\r")
		if line == "" {
			if err := flush(); err != nil {
				return err
			}
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, ok := strings.Cut(line, ":")
		if !ok {
			field = line
			value = ""
		}
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventName = value
		case "data":
			dataLines = append(dataLines, value)
		case "id", "retry":
		}
	}
}

type StreamingResponse struct {
	StatusCode int
	Header     http.Header
	Body       io.ReadCloser
}

func (t HTTPTransport) StreamingDo(
	ctx context.Context,
	endpoint string,
	body []byte,
	auth Auth,
	streamMediaType string,
) (StreamingResponse, error) {
	req, err := t.newRequest(ctx, endpoint, body, auth, streamMediaType)
	if err != nil {
		return StreamingResponse{}, err
	}
	httpClient := t.httpClient()
	resp, err := httpClient.Do(req)
	if err != nil {
		if resp == nil {
			return StreamingResponse{}, err
		}
		closeErr := resp.Body.Close()
		return StreamingResponse{
			StatusCode: resp.StatusCode,
			Header:     resp.Header,
		}, errors.Join(err, closeErr)
	}
	return StreamingResponse{StatusCode: resp.StatusCode, Header: resp.Header, Body: resp.Body}, nil
}

func ReadAllAndClose(resp StreamingResponse, maxBytes int64) ([]byte, bool, error) {
	if resp.Body == nil {
		return nil, false, errors.New("nil streaming response body")
	}
	if maxBytes <= 0 {
		return nil, false, errors.New("streaming response byte limit must be positive")
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
	if err != nil {
		return nil, false, fmt.Errorf("read streaming response body: %w", err)
	}
	if int64(len(body)) > maxBytes {
		return body[:maxBytes], true, nil
	}
	return body, false, nil
}

type responseLimitReader struct {
	reader    io.Reader
	remaining int64
	exceeded  bool
}

func (r *responseLimitReader) Read(p []byte) (int, error) {
	if r.remaining < 0 {
		return 0, ErrProviderResponseTooLarge
	}
	if r.remaining == 0 {
		var probe [1]byte
		n, err := r.reader.Read(probe[:])
		if n > 0 {
			r.remaining = -1
			r.exceeded = true
			return 0, ErrProviderResponseTooLarge
		}
		return 0, err
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	n, err := r.reader.Read(p)
	r.remaining -= int64(n)
	return n, err
}
