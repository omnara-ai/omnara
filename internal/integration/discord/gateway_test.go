package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
)

type gatewayPacket struct {
	Op   int             `json:"op"`
	Data json.RawMessage `json:"d"`
}

func writePacket(t *testing.T, ctx context.Context, conn *websocket.Conn, body string) {
	t.Helper()
	if err := conn.Write(ctx, websocket.MessageText, []byte(body)); err != nil {
		t.Error(err)
	}
}

func readPacket(t *testing.T, ctx context.Context, conn *websocket.Conn) gatewayPacket {
	t.Helper()
	_, body, err := conn.Read(ctx)
	if err != nil {
		t.Error(err)
		return gatewayPacket{}
	}
	var packet gatewayPacket
	if err := json.Unmarshal(body, &packet); err != nil {
		t.Error(err)
	}
	return packet
}

func gatewayFixture(t *testing.T, handler func(context.Context, *websocket.Conn)) (ShardConfig, *atomic.Int32) {
	t.Helper()
	var connections atomic.Int32
	var handlers sync.WaitGroup
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.Add(1)
		defer handlers.Done()
		connections.Add(1)
		if r.Header.Get("Authorization") != "" || r.URL.Query().Get("v") != "10" ||
			r.URL.Query().Get("encoding") != "json" {
			t.Error("invalid gateway handshake")
		}
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			t.Error(err)
			return
		}
		defer conn.CloseNow()
		ctx, cancel := context.WithTimeout(r.Context(), 3*time.Second)
		defer cancel()
		handler(ctx, conn)
	}))
	t.Cleanup(func() {
		server.Close()
		handlers.Wait()
	})
	return ShardConfig{Credentials: credentials(), ShardCount: 1,
		GatewayURL: strings.Replace(server.URL, "http://", "ws://", 1), HTTPClient: server.Client(),
		BeforeIdentify:  func(context.Context) error { return nil },
		heartbeatJitter: func() float64 { return 1 }}, &connections
}

func waitGatewayBarrier(t *testing.T, ctx context.Context, barrier <-chan struct{}) bool {
	t.Helper()
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	select {
	case <-barrier:
		return true
	case <-ctx.Done():
		t.Errorf("gateway barrier: %v", ctx.Err())
		return false
	}
}

func resumeCheckpoint(config ShardConfig) *Checkpoint {
	return &Checkpoint{ApplicationID: "111", BotUserID: "222", ShardCount: 1,
		SessionID: "durable-session", ResumeURL: config.GatewayURL, Sequence: 10}
}

func waitRun(t *testing.T, done <-chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(3 * time.Second):
		t.Fatal("gateway run did not stop")
		return nil
	}
}

func TestGatewayDurableFailureStopsAndRestoresPersistedSequence(t *testing.T) {
	var phase atomic.Int32
	secondSent, replayCommitted := make(chan struct{}), make(chan struct{})
	config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
		writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":600000}}`)
		auth := readPacket(t, ctx, conn)
		var resume struct {
			Token    string `json:"token"`
			Session  string `json:"session_id"`
			Sequence int64  `json:"seq"`
		}
		if json.Unmarshal(auth.Data, &resume) != nil || auth.Op != 6 || resume.Sequence != 10 ||
			resume.Session != "durable-session" || resume.Token != "test-token" {
			t.Errorf("bad resume: %+v", resume)
		}
		writePacket(t, ctx, conn, `{"op":0,"s":11,"t":"MESSAGE_CREATE","d":{"id":"666"}}`)
		if phase.Add(1) == 1 {
			writePacket(t, ctx, conn, `{"op":0,"s":12,"t":"MESSAGE_CREATE","d":{"id":"777"}}`)
			close(secondSent)
		} else {
			if !waitGatewayBarrier(t, ctx, replayCommitted) {
				return
			}
			writePacket(t, ctx, conn, `{"op":7,"d":null}`)
		}
		_, _, _ = conn.Read(ctx) // Wait for the run to close its socket.
	})
	persisted := resumeCheckpoint(config)
	entered, release := make(chan struct{}), make(chan struct{})
	releaseCommit := sync.OnceFunc(func() { close(release) })
	defer releaseCommit()
	var calls atomic.Int32
	done := make(chan error, 1)
	failure := errors.New("durable inbox unavailable")
	go func() {
		done <- RunShard(t.Context(), config, persisted, func(ctx context.Context, dispatch Dispatch, next Checkpoint) error {
			calls.Add(1)
			if dispatch.Sequence != 11 || next.Sequence != 11 {
				t.Error("wrong checkpoint proposal")
			}
			close(entered)
			<-release
			return failure
		})
	}()
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("no intake callback")
	}
	select {
	case <-secondSent:
	case <-time.After(time.Second):
		t.Fatal("second dispatch was not sent")
	}
	if persisted.Sequence != 10 || calls.Load() != 1 {
		t.Fatal("advanced durable checkpoint before commit")
	}
	releaseCommit()
	if err := waitRun(t, done); !errors.Is(err, failure) {
		t.Fatalf("lost intake error: %v", err)
	}
	if calls.Load() != 1 || connections.Load() != 1 || persisted.Sequence != 10 {
		t.Fatal("committed past failed write or automatically reconnected")
	}
	var saved Checkpoint
	err := RunShard(t.Context(), config, persisted, func(ctx context.Context, dispatch Dispatch, next Checkpoint) error {
		saved = next
		close(replayCommitted)
		return nil
	})
	var stopped *GatewayError
	if !errors.As(err, &stopped) || saved.Sequence != 11 || connections.Load() != 2 {
		t.Fatalf("resume result = %+v, error = %v", saved, err)
	}
}

func TestGatewayReadyChecksIdentityAndCommitsCheckpoint(t *testing.T) {
	var resumeURL atomic.Value
	var permits atomic.Int32
	committed := make(chan struct{})
	config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
		writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":600000}}`)
		auth := readPacket(t, ctx, conn)
		var identify struct {
			Intents int    `json:"intents"`
			Shard   []int  `json:"shard"`
			Token   string `json:"token"`
		}
		// Only GUILD_MESSAGES (512) and MESSAGE_CONTENT (32768) are needed.
		if json.Unmarshal(auth.Data, &identify) != nil || auth.Op != 2 || identify.Intents != 512+32768 ||
			len(identify.Shard) != 2 || identify.Shard[0] != 0 || identify.Shard[1] != 1 || permits.Load() != 1 {
			t.Error("identify was not correctly gated")
		}
		ready := `{"op":0,"s":1,"t":"READY","d":{"session_id":"new-session","resume_gateway_url":%q,"user":{"id":"222","bot":true},"application":{"id":"111"}}}`
		writePacket(t, ctx, conn, fmt.Sprintf(ready, resumeURL.Load()))
		writePacket(t, ctx, conn, `{"op":0,"s":2,"t":"MESSAGE_CREATE","d":{"id":"666"}}`)
		if !waitGatewayBarrier(t, ctx, committed) {
			return
		}
		writePacket(t, ctx, conn, `{"op":7,"d":null}`)
		_, _, _ = conn.Read(ctx)
	})
	resumeURL.Store(config.GatewayURL)
	config.BeforeIdentify = func(context.Context) error { permits.Add(1); return nil }
	var checkpoints []Checkpoint
	err := RunShard(t.Context(), config, nil, func(ctx context.Context, dispatch Dispatch, next Checkpoint) error {
		checkpoints = append(checkpoints, next)
		if dispatch.Sequence == 2 {
			close(committed)
		}
		return nil
	})
	var stopped *GatewayError
	if !errors.As(err, &stopped) || len(checkpoints) != 2 || checkpoints[0].SessionID != "new-session" ||
		checkpoints[0].Sequence != 1 || checkpoints[1].Sequence != 2 || connections.Load() != 1 {
		t.Fatalf("checkpoints=%+v, error=%v", checkpoints, err)
	}
}

func TestGatewayDisconnectsNeverAutoReconnect(t *testing.T) {
	for _, mode := range []string{"socket", "op7", "op9 true", "op9 false", "heartbeat", "4004", "4007", "4009", "4014"} {
		t.Run(mode, func(t *testing.T) {
			config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				interval := 600000
				if mode == "heartbeat" {
					interval = 5
				}
				writePacket(t, ctx, conn, fmt.Sprintf(`{"op":10,"d":{"heartbeat_interval":%d}}`, interval))
				_ = readPacket(t, ctx, conn)
				switch mode {
				case "socket":
					return
				case "op7":
					writePacket(t, ctx, conn, `{"op":7,"d":null}`)
				case "op9 true":
					writePacket(t, ctx, conn, `{"op":9,"d":true}`)
				case "op9 false":
					writePacket(t, ctx, conn, `{"op":9,"d":false}`)
				case "4004":
					conn.Close(4004, "private credential detail")
				case "4007":
					conn.Close(4007, "invalid sequence")
				case "4009":
					conn.Close(4009, "expired session")
				case "4014":
					conn.Close(4014, "disallowed intents")
				}
				for {
					if _, _, err := conn.Read(ctx); err != nil {
						return
					}
				}
			})
			err := RunShard(t.Context(), config, resumeCheckpoint(config), func(context.Context, Dispatch, Checkpoint) error {
				t.Error("unexpected dispatch")
				return nil
			})
			var stopped *GatewayError
			if !errors.As(err, &stopped) || connections.Load() != 1 {
				t.Fatalf("error=%v, connections=%d", err, connections.Load())
			}
			wantReset := mode == "op9 false" || mode == "4007" || mode == "4009"
			wantFatal := mode == "4004" || mode == "4014"
			if stopped.ResetSession != wantReset || stopped.Fatal != wantFatal || strings.Contains(err.Error(), "private") {
				t.Fatalf("wrong disconnect classification: %+v", stopped)
			}
		})
	}
}

func TestGatewayIntakePanicAndCancellation(t *testing.T) {
	for _, panicIntake := range []bool{false, true} {
		t.Run(fmt.Sprint(panicIntake), func(t *testing.T) {
			config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":600000}}`)
				_ = readPacket(t, ctx, conn)
				writePacket(t, ctx, conn, `{"op":0,"s":11,"t":"MESSAGE_CREATE","d":{}}`)
				writePacket(t, ctx, conn, `{"op":0,"s":12,"t":"MESSAGE_CREATE","d":{}}`)
				_, _, _ = conn.Read(ctx)
			})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			var calls int
			commit := func(ctx context.Context, event Dispatch, cp Checkpoint) error {
				calls++
				if panicIntake {
					panic("secret application error")
				}
				cancel()
				<-ctx.Done()
				return ctx.Err()
			}
			err := RunShard(ctx, config, resumeCheckpoint(config), commit)
			if err == nil || calls != 1 || connections.Load() != 1 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("error=%v, calls=%d", err, calls)
			}
		})
	}
}

func TestGatewayURLCheckpointAndIdentifyRequirements(t *testing.T) {
	for _, raw := range []string{"wss://evil.example", "wss://discord.gg.evil.example", "wss://u:p@gateway.discord.gg",
		"wss://gateway.discord.gg?encoding=etf", "wss://gateway.discord.gg?compress=zlib-stream", "ws://gateway.discord.gg"} {
		if _, err := gatewayURL(raw, false); err == nil {
			t.Errorf("accepted %s", raw)
		}
	}
	config := ShardConfig{Credentials: credentials(), ShardCount: 1, GatewayURL: "wss://gateway.discord.gg"}
	commit := func(context.Context, Dispatch, Checkpoint) error { return nil }
	if err := RunShard(t.Context(), config, nil, commit); err == nil {
		t.Fatal("missing identify limiter accepted")
	}
	cp := resumeCheckpoint(config)
	cp.BotUserID = "111"
	if err := RunShard(t.Context(), config, cp, commit); err == nil {
		t.Fatal("application ID substituted for bot user ID")
	}
	config.BeforeConnect = func(context.Context) error { return context.Canceled }
	if err := RunShard(t.Context(), config, resumeCheckpoint(config), commit); !errors.Is(err, context.Canceled) {
		t.Fatal("before-connect authority failure was ignored")
	}
}

func TestGatewayRejectsWrongReadyIdentityAndRepeatedHello(t *testing.T) {
	for _, mode := range []string{"application", "bot", "repeated hello"} {
		t.Run(mode, func(t *testing.T) {
			config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":600000}}`)
				_ = readPacket(t, ctx, conn)
				if mode == "repeated hello" {
					writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":600000}}`)
				} else {
					appID, botID := "111", "222"
					if mode == "application" {
						appID = "333"
					} else {
						botID = "111"
					}
					ready := `{"op":0,"s":11,"t":"READY","d":{"session_id":"session","resume_gateway_url":"wss://gateway.discord.gg","user":{"id":%q,"bot":true},"application":{"id":%q}}}`
					writePacket(t, ctx, conn, fmt.Sprintf(ready, botID, appID))
				}
				if _, _, err := conn.Read(ctx); err == nil {
					t.Error("sent another authentication message")
				}
			})
			err := RunShard(t.Context(), config, resumeCheckpoint(config), func(context.Context, Dispatch, Checkpoint) error {
				t.Error("accepted incorrect session identity")
				return nil
			})
			if err == nil || connections.Load() != 1 {
				t.Fatal("invalid session continued")
			}
			if mode != "repeated hello" {
				requireAPIError(t, err, ScopeMismatch)
			}
		})
	}
}

func TestIdentifyPermitFailureStopsBeforeIdentify(t *testing.T) {
	config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
		writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":600000}}`)
		if _, _, err := conn.Read(ctx); err == nil {
			t.Error("sent without identify permit")
		}
	})
	failure := &APIError{Code: RateLimited, RetryAfter: 5 * time.Second}
	config.BeforeIdentify = func(context.Context) error { return failure }
	err := RunShard(t.Context(), config, nil, func(context.Context, Dispatch, Checkpoint) error { return nil })
	if !errors.Is(err, failure) || connections.Load() != 1 {
		t.Fatalf("identify error=%v", err)
	}
}

func TestGatewayBotMetadata(t *testing.T) {
	client, _ := testClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v10/gateway/bot" || r.Header.Get("Authorization") != "Bot test-token" {
			t.Error("wrong gateway discovery request")
		}
		fmt.Fprint(w,
			`{"url":"wss://gateway.discord.gg","shards":2,"session_start_limit":{"total":1000,"remaining":998,"reset_after":100000,"max_concurrency":1}}`)
	})
	info, err := client.GetGatewayBot(t.Context())
	if err != nil || info.Shards != 2 || info.SessionStartLimit.Remaining != 998 ||
		info.SessionStartLimit.MaxConcurrency != 1 {
		t.Fatalf("metadata=%+v, error=%v", info, err)
	}
}

func TestReadyDoesNotCarrySequenceAcrossSessions(t *testing.T) {
	committed := make(chan struct{})
	config, _ := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
		writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":600000}}`)
		_ = readPacket(t, ctx, conn)
		writePacket(t, ctx, conn,
			`{"op":0,"s":1,"t":"READY","d":{"session_id":"new-session","resume_gateway_url":"wss://gateway.discord.gg","user":{"id":"222","bot":true},"application":{"id":"111"}}}`)
		if !waitGatewayBarrier(t, ctx, committed) {
			return
		}
		writePacket(t, ctx, conn, `{"op":7,"d":null}`)
		_, _, _ = conn.Read(ctx)
	})
	var saved Checkpoint
	commit := func(ctx context.Context, event Dispatch, next Checkpoint) error {
		saved = next
		close(committed)
		return nil
	}
	_ = RunShard(t.Context(), config, resumeCheckpoint(config), commit)
	if saved.SessionID != "new-session" || saved.Sequence != 1 {
		t.Fatalf("new session checkpoint=%+v", saved)
	}
}
