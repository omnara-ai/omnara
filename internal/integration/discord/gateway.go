package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/discord-go/discord.go/gateway"
	"github.com/discord-go/discord.go/intents"
	"github.com/omnara-ai/omnara/internal/outboundhttp"
)

// ThreadBotIntents requests guild/channel metadata, guild messages and message
// content for replies without mentions. MESSAGE_CONTENT must also be enabled
// (and approved when required) in the customer's Discord developer portal.
const ThreadBotIntents = 1 + 512 + 32768

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
	// It runs immediately before the SDK sends IDENTIFY, not on RESUME.
	BeforeIdentify func(context.Context) error
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

// RunShard performs exactly one connection. It never reconnects itself. Each
// subsequent run MUST load a fresh durable checkpoint, never the SDK's received
// sequence. commit blocks the SDK dispatch loop; error/panic ends this run before
// another event is read. No provider mutation occurs on cancellation/lease loss.
func RunShard(ctx context.Context, config ShardConfig, persisted *Checkpoint, commit CommitDispatch) error {
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
	// websocket.Dial owns and closes the handshake response body on all paths.
	//nolint:bodyclose // websocket.Dial owns the response body.
	socket, _, err := websocket.Dial(dialCtx, target, &websocket.DialOptions{HTTPClient: httpClient})
	dialCancel()
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		return &GatewayError{RetryAfter: time.Second}
	}
	defer func() { _ = socket.CloseNow() }()
	socket.SetReadLimit(ResponseMaxBytes)
	var closeCode int
	var callbackErr error
	var identifyErr error
	var helloSeen bool
	conn := &sdkConnection{
		read: func() ([]byte, error) {
			_, data, err := socket.Read(ctx)
			if err != nil {
				if code := websocket.CloseStatus(err); code >= 0 {
					closeCode = int(code)
				}
				return nil, err
			}
			// Validate HELLO before the SDK creates its ticker. This also forbids
			// another HELLO from causing an in-session automatic RESUME.
			var envelope struct {
				Op   int             `json:"op"`
				Data json.RawMessage `json:"d"`
			}
			if json.Unmarshal(data, &envelope) != nil {
				return nil, invalidResponse(false)
			}
			if !helloSeen && envelope.Op != 10 {
				return nil, invalidResponse(false)
			}
			if envelope.Op == 10 {
				var hello struct {
					Interval int `json:"heartbeat_interval"`
				}
				if helloSeen || json.Unmarshal(envelope.Data, &hello) != nil || hello.Interval < 1 || hello.Interval > 600000 {
					return nil, invalidResponse(false)
				}
				helloSeen = true
			}
			return data, nil
		},
		write: func(data []byte) error {
			var payload struct {
				Op int `json:"op"`
			}
			if err := json.Unmarshal(data, &payload); err != nil {
				return err
			}
			if payload.Op == 2 {
				if config.BeforeIdentify == nil {
					return errors.New("discord identify permit missing")
				}
				permitCtx, permitCancel := context.WithTimeout(ctx, OperationTimeout)
				err := config.BeforeIdentify(permitCtx)
				if err == nil {
					err = permitCtx.Err()
				}
				permitCancel()
				if err != nil {
					identifyErr = err
					return err
				}
			}
			writeCtx, writeCancel := context.WithTimeout(ctx, OperationTimeout)
			defer writeCancel()
			return socket.Write(writeCtx, websocket.MessageText, data)
		},
		close: socket.CloseNow,
	}
	dispatcher := gateway.NewDispatcher()
	sdk := gateway.NewClient(conn, dispatcher)
	sdk.SetToken(config.Credentials.BotToken)
	sdk.Intents = intents.Intent(ThreadBotIntents)
	sdk.Shard = []int{config.ShardID, config.ShardCount}
	sdk.Cache = nil
	sdk.Session = gateway.NewSession()
	if persisted != nil {
		sdk.Session.SetSessionID(persisted.SessionID)
		sdk.Session.SetResumeURL(target)
		sdk.Session.UpdateSequence(persisted.Sequence)
	}
	// Supported public control: Start returns on disconnect when no factory is
	// configured. No synthetic errors, SDK state rewinds or internal field access.
	sdk.ConnFactory = nil
	var invalidSession *bool
	dispatcher.AddHandler(func(raw []byte) {
		defer func() {
			if recover() != nil {
				callbackErr = errors.New("discord durable intake panicked")
				cancel()
			}
		}()
		var envelope struct {
			Op       int             `json:"op"`
			Sequence *int64          `json:"s"`
			Type     string          `json:"t"`
			Data     json.RawMessage `json:"d"`
		}
		if json.Unmarshal(raw, &envelope) != nil {
			callbackErr = invalidResponse(false)
			cancel()
			return
		}
		if envelope.Op == 9 {
			var resumable bool
			_ = json.Unmarshal(envelope.Data, &resumable)
			invalidSession = &resumable
			return
		}
		if envelope.Op != 0 {
			return
		}
		if envelope.Sequence == nil || *envelope.Sequence < 0 || envelope.Type == "" {
			callbackErr = invalidResponse(false)
			cancel()
			return
		}
		next := checkpoint
		if envelope.Type == "READY" {
			var ready struct {
				SessionID   string `json:"session_id"`
				ResumeURL   string `json:"resume_gateway_url"`
				User        User   `json:"user"`
				Application struct {
					ID string `json:"id"`
				} `json:"application"`
			}
			if json.Unmarshal(envelope.Data, &ready) != nil || ready.SessionID == "" || len(ready.SessionID) > 256 ||
				ready.User.ID != config.Credentials.BotUserID || !ready.User.Bot ||
				ready.Application.ID != config.Credentials.ApplicationID {
				callbackErr = &APIError{Code: ScopeMismatch}
				cancel()
				return
			}
			resumeURL, err := gatewayURL(ready.ResumeURL, config.HTTPClient != nil)
			if err != nil {
				callbackErr = err
				cancel()
				return
			}
			next.SessionID, next.ResumeURL = ready.SessionID, resumeURL
			// READY establishes a new session; its sequence is independent of
			// the checkpoint supplied for a previous session's RESUME attempt.
			next.Sequence = -1
		}
		if next.SessionID == "" {
			callbackErr = invalidResponse(false)
			cancel()
			return
		}
		next.Sequence = max(next.Sequence, *envelope.Sequence)
		commitCtx, commitCancel := context.WithTimeout(ctx, OperationTimeout)
		err := commit(commitCtx, Dispatch{Type: envelope.Type, Sequence: *envelope.Sequence, Data: envelope.Data}, next)
		if err == nil {
			err = commitCtx.Err()
		}
		commitCancel()
		if err != nil {
			callbackErr = err
			cancel()
			return
		}
		checkpoint = next
	})
	err = sdk.Start(ctx)
	if callbackErr != nil {
		return callbackErr
	}
	if identifyErr != nil {
		return identifyErr
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	result := &GatewayError{CloseCode: closeCode, RetryAfter: time.Second}
	switch closeCode {
	case 4004, 4010, 4011, 4012, 4013, 4014:
		result.Fatal = true
	case 4007, 4009:
		result.ResetSession = true
	}
	if invalidSession != nil {
		result.ResetSession = !*invalidSession
		result.RetryAfter = 5 * time.Second
	}
	if errors.Is(err, gateway.ErrFatalClose) {
		result.Fatal = true
	}
	return result
}

// sdkConnection adapts the already-maintained coder/websocket transport to the
// SDK's documented Connection interface; the SDK owns the Gateway protocol.
type sdkConnection struct {
	read  func() ([]byte, error)
	write func([]byte) error
	close func() error
}

func (c *sdkConnection) Read() ([]byte, error)   { return c.read() }
func (c *sdkConnection) Write(data []byte) error { return c.write(data) }
func (c *sdkConnection) Close() error            { return c.close() }

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
