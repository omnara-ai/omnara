package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand/v2"
	"net/http"
	"net/url"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

// ThreadBotIntents requests guild messages and message content for replies
// without mentions. Channel metadata is fetched through REST, not a guild cache.
// MESSAGE_CONTENT must also be enabled
// (and approved when required) in the customer's Discord developer portal.
const ThreadBotIntents = 512 + 32768

type GatewayInfo struct {
	URL               string            `json:"url"`
	Shards            int               `json:"shards"`
	SessionStartLimit SessionStartLimit `json:"session_start_limit"`
}

type SessionStartLimit struct {
	Total            int   `json:"total"`
	Remaining        int   `json:"remaining"`
	ResetAfterMillis int64 `json:"reset_after"`
	MaxConcurrency   int   `json:"max_concurrency"`
}

func (c *Client) GetGatewayBot(ctx context.Context) (GatewayInfo, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	var info GatewayInfo
	if err := c.json(ctx, http.MethodGet, "/gateway/bot", nil, &info, false); err != nil {
		return GatewayInfo{}, err
	}
	if _, err := gatewayURL(info.URL, false); err != nil || info.Shards < 1 ||
		info.SessionStartLimit.MaxConcurrency < 1 || info.SessionStartLimit.Remaining < 0 ||
		info.SessionStartLimit.ResetAfterMillis < 0 {
		return GatewayInfo{}, invalidResponse(false)
	}
	return info, nil
}

// Checkpoint belongs to one connection credential revision and shard topology.
// The caller must fence ownership and persist this atomically with dispatch intake.
type Checkpoint struct {
	ApplicationID string `json:"application_id"`
	BotUserID     string `json:"bot_user_id"`
	ShardID       int    `json:"shard_id"`
	ShardCount    int    `json:"shard_count"`
	SessionID     string `json:"session_id"`
	ResumeURL     string `json:"resume_url"`
	Sequence      int64  `json:"sequence"`
}

type ShardConfig struct {
	Credentials Credentials
	ShardID     int
	ShardCount  int
	// GatewayURL comes from GetGatewayBot, not customer-authored arbitrary URLs.
	GatewayURL string
	// HTTPClient is a trusted transport/test setting. Redirects are forbidden.
	HTTPClient    *http.Client
	BeforeConnect func(context.Context) error
	// BeforeIdentify must obtain the worker's bot-wide IDENTIFY rate permit using
	// session_start_limit, including shard_id % max_concurrency bucket spacing.
	// It runs immediately before IDENTIFY, not on RESUME.
	BeforeIdentify func(context.Context) error

	// Tests can fix the first heartbeat delay without changing the protocol clock.
	heartbeatJitter func() float64
	helloTimeout    time.Duration
}

type CommitDispatch func(context.Context, Dispatch, Checkpoint) error

type GatewayError struct {
	// ResetSession means Discord cannot resume. The caller must durably record
	// that fact before a fresh IDENTIFY; old-session replay may be lost.
	ResetSession bool
	Fatal        bool
	CloseCode    int
	RetryAfter   time.Duration
}

func (e *GatewayError) Error() string {
	return fmt.Sprintf("discord gateway stopped (close %d, reset %t, fatal %t)", e.CloseCode, e.ResetSession, e.Fatal)
}

// RunShard performs exactly one connection. Every subsequent run MUST reload the
// durable checkpoint. Heartbeats report received sequence; only committed events
// advance the checkpoint used for RESUME. Database work is sequential and bounded
// separately from heartbeats; backpressure can delay incoming control frames.
func RunShard(ctx context.Context, config ShardConfig, persisted *Checkpoint, commit CommitDispatch) (runErr error) {
	if !validID(config.Credentials.ApplicationID) || !validID(config.Credentials.BotUserID) ||
		config.Credentials.BotToken == "" || strings.ContainsAny(config.Credentials.BotToken, " \r\n\t") ||
		config.ShardCount < 1 || config.ShardID < 0 || config.ShardID >= config.ShardCount || commit == nil {
		return errors.New("invalid discord shard configuration")
	}
	checkpoint := Checkpoint{ApplicationID: config.Credentials.ApplicationID, BotUserID: config.Credentials.BotUserID,
		ShardID: config.ShardID, ShardCount: config.ShardCount, Sequence: -1}
	rawURL := config.GatewayURL
	if persisted != nil {
		if persisted.ApplicationID != checkpoint.ApplicationID || persisted.BotUserID != checkpoint.BotUserID ||
			persisted.ShardID != config.ShardID || persisted.ShardCount != config.ShardCount ||
			persisted.Sequence < 0 || persisted.SessionID == "" || len(persisted.SessionID) > 256 {
			return errors.New("discord checkpoint does not match shard identity")
		}
		checkpoint, rawURL = *persisted, persisted.ResumeURL
	} else if config.BeforeIdentify == nil {
		return errors.New("discord fresh session requires identify rate permit callback")
	}
	target, err := gatewayURL(rawURL, config.HTTPClient != nil)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	if config.BeforeConnect != nil {
		if err := config.BeforeConnect(ctx); err != nil {
			return err
		}
	}
	httpClient := config.HTTPClient
	if httpClient == nil {
		httpClient = outboundhttp.NewPublicClient(outboundhttp.PublicClientOptions{})
	}
	httpClient = outboundhttp.CloneWithoutRedirects(httpClient)
	dialCtx, dialCancel := context.WithTimeout(ctx, OperationTimeout)
	//nolint:bodyclose // websocket.Dial owns the handshake response body.
	socket, _, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{HTTPClient: httpClient})
	dialCancel()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &GatewayError{RetryAfter: time.Second}
	}
	var jobs sync.WaitGroup
	var workerErr error // Read only after joining the worker.
	defer func() {
		cancel()
		// Discord invalidates sessions closed with 1000/1001. TCP close retains
		// resumability for shutdown, lease loss, and protocol failures alike.
		_ = socket.CloseNow()
		jobs.Wait()
		// A protocol stop may race an independent intake/IDENTIFY failure. Keep
		// both causes (especially reset/fatal signals), not our own cancellation.
		if workerErr != nil && !errors.Is(workerErr, context.Canceled) && !errors.Is(runErr, workerErr) {
			if errors.Is(runErr, context.Canceled) {
				runErr = workerErr
			} else {
				runErr = errors.Join(runErr, workerErr)
			}
		}
	}()
	socket.SetReadLimit(ResponseMaxBytes)
	helloTimeout := config.helloTimeout
	if helloTimeout <= 0 {
		helloTimeout = OperationTimeout
	}
	helloCtx, helloCancel := context.WithTimeout(ctx, helloTimeout)
	hello, err := readGatewayEvent(helloCtx, socket)
	helloCancel()
	if err != nil {
		return gatewayConnectionError(ctx, err)
	}
	// Discord can request reconnection at any point, even before HELLO.
	if hello.Op == 7 {
		return &GatewayError{RetryAfter: time.Second}
	}
	var timing struct {
		Interval int `json:"heartbeat_interval"`
	}
	if hello.Op != 10 || json.Unmarshal(hello.Data, &timing) != nil || timing.Interval < 1 || timing.Interval > 600000 {
		return invalidResponse(false)
	}
	interval := time.Duration(timing.Interval) * time.Millisecond
	jitter := rand.Float64()
	if config.heartbeatJitter != nil {
		jitter = config.heartbeatJitter()
	}
	heartbeat := time.NewTimer(time.Duration(float64(interval) * jitter))
	defer heartbeat.Stop()

	// Backpressure allows replay bursts to drain without reconnecting repeatedly.
	// The worker, loop and reader each retain at most one bounded dispatch frame.
	dispatches := make(chan gatewayEvent)
	committed := make(chan error, 1)
	jobs.Go(func() {
		checkpoint := checkpoint // Durable progress belongs only to the intake worker.
		err := authenticateGateway(ctx, socket, config, checkpoint)
		if err == nil {
			for {
				select {
				case <-ctx.Done():
					err = ctx.Err()
				case event := <-dispatches:
					checkpoint, err = commitGatewayEvent(ctx, config, checkpoint, event, commit)
				}
				if err != nil {
					break
				}
			}
		}
		workerErr = err
		committed <- err
	})
	type readResult struct {
		event gatewayEvent
		err   error
	}
	incoming := make(chan readResult)
	jobs.Go(func() {
		for {
			event, err := readGatewayEvent(ctx, socket)
			select {
			case incoming <- readResult{event, err}:
			case <-ctx.Done():
				return
			}
			if err != nil {
				return
			}
		}
	})
	sequence := int64(-1)
	if persisted != nil {
		sequence = persisted.Sequence
	}
	awaitingACK, pausedSinceBeat, usedACKGrace := false, false, false
	var pending *gatewayEvent
	sendHeartbeat := func() error {
		var value *int64
		if sequence >= 0 {
			value = &sequence
		}
		return writeGatewayEvent(ctx, socket, 1, value)
	}
	for {
		readFrom := incoming
		var sendTo chan gatewayEvent
		var next gatewayEvent
		if pending != nil {
			readFrom, sendTo, next = nil, dispatches, *pending
			pausedSinceBeat = true
		}
		select {
		case sendTo <- next:
			pending = nil
		case <-ctx.Done():
			return ctx.Err()
		case err := <-committed:
			return err
		case <-heartbeat.C:
			// An ACK may be behind dispatches while intake is backpressured.
			// Allow one extra interval; continuous backpressure must not disable
			// the dead-connection watchdog indefinitely.
			if awaitingACK {
				if !pausedSinceBeat || usedACKGrace {
					return &GatewayError{RetryAfter: time.Second}
				}
				usedACKGrace = true
			}
			if err := sendHeartbeat(); err != nil {
				return err
			}
			awaitingACK = true
			pausedSinceBeat = pending != nil
			heartbeat.Reset(interval)
		case result := <-readFrom:
			if result.err != nil {
				return gatewayConnectionError(ctx, result.err)
			}
			event := result.event
			switch event.Op {
			case 0:
				if event.Sequence == nil || *event.Sequence < 0 || event.Type == "" {
					return invalidResponse(false)
				}
				sequence = *event.Sequence
				pending = &event
			case 1:
				if err := sendHeartbeat(); err != nil {
					return err
				}
			case 7:
				return &GatewayError{RetryAfter: time.Second}
			case 9:
				var resumable bool
				if json.Unmarshal(event.Data, &resumable) != nil {
					return invalidResponse(false)
				}
				return &GatewayError{ResetSession: !resumable, RetryAfter: 5 * time.Second}
			case 10:
				return invalidResponse(false)
			case 11:
				awaitingACK, usedACKGrace = false, false
			}
		}
	}
}

type gatewayEvent struct {
	Op       int             `json:"op"`
	Sequence *int64          `json:"s"`
	Type     string          `json:"t"`
	Data     json.RawMessage `json:"d"`
}

func readGatewayEvent(ctx context.Context, socket *websocket.Conn) (gatewayEvent, error) {
	_, raw, err := socket.Read(ctx)
	if err != nil {
		return gatewayEvent{}, err
	}
	event := gatewayEvent{Op: -1}
	if json.Unmarshal(raw, &event) != nil || event.Op < 0 {
		return gatewayEvent{}, invalidResponse(false)
	}
	return event, nil
}

func gatewayConnectionError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	var apiError *APIError
	if errors.As(err, &apiError) {
		return apiError
	}
	result := &GatewayError{RetryAfter: time.Second}
	if code := websocket.CloseStatus(err); code >= 0 {
		result.CloseCode = int(code)
	}
	switch result.CloseCode {
	case 4004, 4010, 4011, 4012, 4013, 4014:
		result.Fatal = true
	case 4007, 4009:
		result.ResetSession = true
	}
	return result
}

func writeGatewayEvent(ctx context.Context, socket *websocket.Conn, op int, data any) error {
	raw, err := json.Marshal(struct {
		Op   int `json:"op"`
		Data any `json:"d"`
	}{op, data})
	if err != nil || len(raw) > 4096 {
		return errors.New("invalid discord gateway send payload")
	}
	writeCtx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if err := socket.Write(writeCtx, websocket.MessageText, raw); err != nil {
		return gatewayConnectionError(ctx, err)
	}
	return nil
}

func authenticateGateway(ctx context.Context, socket *websocket.Conn, config ShardConfig, checkpoint Checkpoint) error {
	if checkpoint.SessionID != "" {
		return writeGatewayEvent(ctx, socket, 6, struct {
			Token     string `json:"token"`
			SessionID string `json:"session_id"`
			Sequence  int64  `json:"seq"`
		}{config.Credentials.BotToken, checkpoint.SessionID, checkpoint.Sequence})
	}
	permitCtx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	if err := config.BeforeIdentify(permitCtx); err != nil {
		return err
	}
	if err := permitCtx.Err(); err != nil {
		return err
	}
	return writeGatewayEvent(ctx, socket, 2, struct {
		Token      string            `json:"token"`
		Intents    int               `json:"intents"`
		Shard      [2]int            `json:"shard"`
		Properties map[string]string `json:"properties"`
	}{config.Credentials.BotToken, ThreadBotIntents, [2]int{config.ShardID, config.ShardCount},
		map[string]string{"os": runtime.GOOS, "browser": "omnara", "device": "omnara"}})
}

func commitGatewayEvent(
	ctx context.Context,
	config ShardConfig,
	checkpoint Checkpoint,
	event gatewayEvent,
	commit CommitDispatch,
) (next Checkpoint, err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("discord durable intake panicked")
		}
	}()
	if err := ctx.Err(); err != nil {
		return checkpoint, err
	}
	next = checkpoint
	if event.Type == "READY" {
		var ready struct {
			SessionID   string `json:"session_id"`
			ResumeURL   string `json:"resume_gateway_url"`
			User        User   `json:"user"`
			Application struct {
				ID string `json:"id"`
			} `json:"application"`
		}
		if json.Unmarshal(event.Data, &ready) != nil || ready.SessionID == "" || len(ready.SessionID) > 256 ||
			ready.User.ID != config.Credentials.BotUserID || !ready.User.Bot ||
			ready.Application.ID != config.Credentials.ApplicationID {
			return checkpoint, &APIError{Code: ScopeMismatch}
		}
		resumeURL, err := gatewayURL(ready.ResumeURL, config.HTTPClient != nil)
		if err != nil {
			return checkpoint, err
		}
		next.SessionID, next.ResumeURL = ready.SessionID, resumeURL
		next.Sequence = -1 // READY establishes a new session, independent of the old sequence.
	}
	if next.SessionID == "" {
		return checkpoint, invalidResponse(false)
	}
	next.Sequence = max(next.Sequence, *event.Sequence)
	commitCtx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	dispatch := Dispatch{Type: event.Type, Sequence: *event.Sequence, Data: event.Data}
	if err := commit(commitCtx, dispatch, next); err != nil {
		return checkpoint, err
	}
	// Cancellation does not prove rollback. Even on this error the next run must
	// reload storage; a commit may have succeeded while cancellation raced it.
	return next, commitCtx.Err()
}

func gatewayURL(raw string, allowTest bool) (string, error) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Fragment != "" || u.RawPath != "" ||
		(u.Path != "" && u.Path != "/") {
		return "", errors.New("invalid discord gateway URL")
	}
	loopback := u.Hostname() == "127.0.0.1" || u.Hostname() == "localhost" || u.Hostname() == "::1"
	public := u.Scheme == "wss" && u.Port() == "" &&
		(u.Host == "gateway.discord.gg" || strings.HasSuffix(u.Host, ".discord.gg"))
	if !public && !(allowTest && u.Scheme == "ws" && loopback) {
		return "", errors.New("invalid discord gateway origin")
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return "", errors.New("invalid discord gateway query")
	}
	for key, values := range query {
		if len(values) != 1 || (key != "v" && key != "encoding") ||
			(key == "v" && values[0] != "10") || (key == "encoding" && values[0] != "json") {
			return "", errors.New("unsupported discord gateway encoding")
		}
	}
	query.Set("v", "10")
	query.Set("encoding", "json")
	u.RawQuery = query.Encode()
	return u.String(), nil
}
