package discord

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/stretchr/testify/require"
)

func expectGatewayHeartbeat(t *testing.T, ctx context.Context, conn *websocket.Conn, sequence string) {
	t.Helper()
	packet := readPacket(t, ctx, conn)
	if packet.Op != 1 || string(packet.Data) != sequence {
		t.Errorf("heartbeat = %+v, want opcode 1 with d=%s", packet, sequence)
	}
}

func requireGatewayRetry(t *testing.T, err error) {
	t.Helper()
	var stopped *GatewayError
	require.ErrorAs(t, err, &stopped)
	require.False(t, stopped.Fatal)
	require.False(t, stopped.ResetSession)
	require.Equal(t, time.Second, stopped.RetryAfter)
}

func TestGatewayHeartbeatsBeforeIdentify(t *testing.T) {
	for _, mode := range []string{"server requested", "zero initial jitter"} {
		t.Run(mode, func(t *testing.T) {
			permitEntered, permitRelease, readyCommitted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":600000}}`)
				if !waitGatewayBarrier(t, ctx, permitEntered) {
					return
				}
				if mode == "server requested" {
					writePacket(t, ctx, conn, `{"op":1,"d":null}`)
				}
				expectGatewayHeartbeat(t, ctx, conn, "null")
				writePacket(t, ctx, conn, `{"op":11,"d":null}`)
				close(permitRelease)
				if auth := readPacket(t, ctx, conn); auth.Op != 2 {
					t.Errorf("expected IDENTIFY after permit, got %+v", auth)
					return
				}
				writePacket(t, ctx, conn, `{"op":0,"s":0,"t":"READY","d":{
					"session_id":"fresh","resume_gateway_url":"wss://gateway.discord.gg",
					"user":{"id":"222","bot":true},"application":{"id":"111"}}}`)
				if !waitGatewayBarrier(t, ctx, readyCommitted) {
					return
				}
				writePacket(t, ctx, conn, `{"op":1,"d":null}`)
				expectGatewayHeartbeat(t, ctx, conn, "0")
				writePacket(t, ctx, conn, `{"op":11,"d":null}`)
				writePacket(t, ctx, conn, `{"op":7,"d":null}`)
				_, _, _ = conn.Read(ctx)
			})
			if mode == "zero initial jitter" {
				config.heartbeatJitter = func() float64 { return 0 }
			}
			config.BeforeIdentify = func(ctx context.Context) error {
				close(permitEntered)
				select {
				case <-permitRelease:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			var saved Checkpoint
			err := RunShard(t.Context(), config, nil, func(_ context.Context, event Dispatch, next Checkpoint) error {
				if event.Type != "READY" || event.Sequence != 0 {
					t.Errorf("unexpected first dispatch: %+v", event)
				}
				saved = next
				close(readyCommitted)
				return nil
			})
			requireGatewayRetry(t, err)
			require.Equal(t, "fresh", saved.SessionID)
			require.Zero(t, saved.Sequence)
			require.EqualValues(t, 1, connections.Load())
		})
	}
}

func TestGatewayHeartbeatsDuringBlockedCommit(t *testing.T) {
	for _, mode := range []string{"scheduled with ACKs", "server requested"} {
		t.Run(mode, func(t *testing.T) {
			entered, exited := make(chan struct{}), make(chan struct{})
			config, _ := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				interval, beats := 100, 3
				if mode == "server requested" {
					interval, beats = 600000, 1
				}
				writePacket(t, ctx, conn, fmt.Sprintf(`{"op":10,"d":{"heartbeat_interval":%d}}`, interval))
				_ = readPacket(t, ctx, conn)
				writePacket(t, ctx, conn, `{"op":0,"s":11,"t":"MESSAGE_CREATE","d":{}}`)
				if !waitGatewayBarrier(t, ctx, entered) {
					return
				}
				if mode == "server requested" {
					writePacket(t, ctx, conn, `{"op":1,"d":null}`)
				}
				for range beats {
					expectGatewayHeartbeat(t, ctx, conn, "11")
					writePacket(t, ctx, conn, `{"op":11,"d":null}`)
				}
				writePacket(t, ctx, conn, `{"op":7,"d":null}`)
				_, _, _ = conn.Read(ctx)
			})
			persisted := resumeCheckpoint(config)
			err := RunShard(t.Context(), config, persisted, func(ctx context.Context, _ Dispatch, _ Checkpoint) error {
				close(entered)
				defer close(exited)
				<-ctx.Done()
				return ctx.Err()
			})
			requireGatewayRetry(t, err)
			require.EqualValues(t, 10, persisted.Sequence)
			select {
			case <-exited:
			default:
				t.Fatal("RunShard returned before its commit worker exited")
			}
		})
	}
}

func TestGatewayServerHeartbeatDoesNotStartACKDeadline(t *testing.T) {
	config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
		writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":200}}`)
		_ = readPacket(t, ctx, conn)
		writePacket(t, ctx, conn, `{"op":1,"d":null}`)
		expectGatewayHeartbeat(t, ctx, conn, "10")
		expectGatewayHeartbeat(t, ctx, conn, "10")
		writePacket(t, ctx, conn, `{"op":11,"d":null}`)
		expectGatewayHeartbeat(t, ctx, conn, "10")
		writePacket(t, ctx, conn, `{"op":11,"d":null}`)
		writePacket(t, ctx, conn, `{"op":7,"d":null}`)
		_, _, _ = conn.Read(ctx)
	})
	err := RunShard(t.Context(), config, resumeCheckpoint(config), func(context.Context, Dispatch, Checkpoint) error {
		t.Error("unexpected dispatch during heartbeat exchange")
		return nil
	})
	requireGatewayRetry(t, err)
	require.EqualValues(t, 1, connections.Load())
}

func TestGatewayReplayBurstBackpressure(t *testing.T) {
	for _, drain := range []bool{true, false} {
		name := "missing ACK stops sustained backlog"
		if drain {
			name = "finite burst drains on one connection"
		}
		t.Run(name, func(t *testing.T) {
			const count = 64
			entered, release, allCommitted := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":200}}`)
				_ = readPacket(t, ctx, conn)
				writePacket(t, ctx, conn, `{"op":0,"s":11,"t":"MESSAGE_CREATE","d":{}}`)
				if !waitGatewayBarrier(t, ctx, entered) {
					return
				}
				for sequence := 12; sequence <= 10+count; sequence++ {
					writePacket(t, ctx, conn, fmt.Sprintf(`{"op":0,"s":%d,"t":"MESSAGE_CREATE","d":{}}`, sequence))
				}
				for range 2 {
					expectGatewayHeartbeat(t, ctx, conn, "12")
					if drain {
						writePacket(t, ctx, conn, `{"op":11,"d":null}`)
					}
				}
				if calls.Load() != 1 {
					t.Error("dispatches committed concurrently with the blocked first write")
				}
				if drain {
					close(release)
					if !waitGatewayBarrier(t, ctx, allCommitted) {
						return
					}
					expectGatewayHeartbeat(t, ctx, conn, "74")
					writePacket(t, ctx, conn, `{"op":11,"d":null}`)
					writePacket(t, ctx, conn, `{"op":7,"d":null}`)
				}
				if _, _, err := conn.Read(ctx); !drain && (err == nil || ctx.Err() != nil) {
					t.Errorf("missing ACK must close after its one grace interval, read=%v context=%v", err, ctx.Err())
				}
			})
			persisted := resumeCheckpoint(config)
			var saved []Checkpoint
			err := RunShard(t.Context(), config, persisted, func(ctx context.Context, event Dispatch, next Checkpoint) error {
				if calls.Add(1) == 1 {
					close(entered)
					select {
					case <-release:
					case <-ctx.Done():
						return ctx.Err()
					}
				}
				saved = append(saved, next)
				if event.Sequence == 10+count {
					close(allCommitted)
				}
				return nil
			})
			requireGatewayRetry(t, err)
			require.EqualValues(t, 1, connections.Load())
			require.EqualValues(t, 10, persisted.Sequence)
			if drain {
				require.Len(t, saved, count)
				for index, checkpoint := range saved {
					require.EqualValues(t, 11+index, checkpoint.Sequence)
				}
			} else {
				require.Empty(t, saved)
				require.EqualValues(t, 1, calls.Load())
			}
		})
	}
}

func TestGatewayCancellationRacingSuccessfulCommitReloadsStorage(t *testing.T) {
	entered, sent, canceled := make(chan struct{}), make(chan struct{}), make(chan struct{})
	finish, exited := make(chan struct{}), make(chan struct{})
	socketClosed, replayCommitted := make(chan struct{}), make(chan struct{})
	finishCommit := sync.OnceFunc(func() { close(finish) })
	defer finishCommit()
	var phase atomic.Int32
	config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
		first := phase.Add(1) == 1
		writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":600000}}`)
		auth := readPacket(t, ctx, conn)
		var resume struct {
			Sequence int64 `json:"seq"`
		}
		wantSequence := int64(10)
		if !first {
			wantSequence = 11
		}
		if json.Unmarshal(auth.Data, &resume) != nil || auth.Op != 6 || resume.Sequence != wantSequence {
			t.Errorf("resume=%+v opcode=%d, want saved sequence %d", resume, auth.Op, wantSequence)
		}
		if first {
			defer close(socketClosed)
			writePacket(t, ctx, conn, `{"op":0,"s":11,"t":"MESSAGE_CREATE","d":{}}`)
			if !waitGatewayBarrier(t, ctx, entered) {
				return
			}
			writePacket(t, ctx, conn, `{"op":0,"s":12,"t":"MESSAGE_CREATE","d":{}}`)
			writePacket(t, ctx, conn, `{"op":0,"s":13,"t":"MESSAGE_CREATE","d":{}}`)
			close(sent)
			_, _, err := conn.Read(ctx)
			code := websocket.CloseStatus(err)
			if err == nil || ctx.Err() != nil || code == websocket.StatusNormalClosure || code == websocket.StatusGoingAway {
				t.Errorf("cancellation must preserve the session with TCP close: code=%d err=%v context=%v", code, err, ctx.Err())
			}
			return
		}
		writePacket(t, ctx, conn, `{"op":0,"s":12,"t":"MESSAGE_CREATE","d":{}}`)
		if !waitGatewayBarrier(t, ctx, replayCommitted) {
			return
		}
		writePacket(t, ctx, conn, `{"op":7,"d":null}`)
		_, _, _ = conn.Read(ctx)
	})
	config.BeforeIdentify = func(context.Context) error {
		t.Error("RESUME consumed an IDENTIFY permit")
		return nil
	}
	persisted := resumeCheckpoint(config)
	saved := *persisted
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- RunShard(ctx, config, persisted, func(ctx context.Context, _ Dispatch, next Checkpoint) error {
			defer close(exited)
			close(entered)
			<-ctx.Done()
			close(canceled)
			<-finish
			saved = next
			return nil
		})
	}()
	require.True(t, waitGatewayBarrier(t, t.Context(), sent))
	cancel()
	require.True(t, waitGatewayBarrier(t, t.Context(), canceled))
	require.True(t, waitGatewayBarrier(t, t.Context(), socketClosed))
	select {
	case err := <-done:
		t.Fatalf("returned before the in-flight commit finished: %v", err)
	default:
	}
	finishCommit()
	require.ErrorIs(t, waitRun(t, done), context.Canceled)
	select {
	case <-exited:
	default:
		t.Fatal("commit worker was not joined")
	}
	require.EqualValues(t, 10, persisted.Sequence)
	require.EqualValues(t, 11, saved.Sequence)
	reloaded := saved
	err := RunShard(t.Context(), config, &reloaded, func(_ context.Context, _ Dispatch, next Checkpoint) error {
		saved = next
		close(replayCommitted)
		return nil
	})
	requireGatewayRetry(t, err)
	require.EqualValues(t, 12, saved.Sequence)
	require.EqualValues(t, 2, connections.Load())
}

func TestGatewayShutdownPreservesIndependentWorkerFailure(t *testing.T) {
	for _, test := range []struct{ worker, stop string }{
		{"intake failure", "reconnect"},
		{"intake panic", "reset"},
		{"gateway worker failure", "reset"},
		{"identify permit", "reconnect"},
		{"intake failure", "fatal"},
		{"intake failure", "cancel"},
	} {
		t.Run(test.worker+"/"+test.stop, func(t *testing.T) {
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, exited := make(chan struct{}), make(chan struct{})
			var calls atomic.Int32
			workerFailure := errors.New("independent durable intake failure")
			if test.worker == "identify permit" {
				workerFailure = &APIError{Code: RateLimited, RetryAfter: 17 * time.Second}
			} else if test.worker == "gateway worker failure" {
				workerFailure = &GatewayError{RetryAfter: time.Second}
			}
			blockedWorker := func(ctx context.Context) error {
				calls.Add(1)
				close(entered)
				defer close(exited)
				<-ctx.Done()
				if test.worker == "intake panic" {
					panic("private intake detail")
				}
				return workerFailure
			}
			config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
				writePacket(t, ctx, conn, `{"op":10,"d":{"heartbeat_interval":600000}}`)
				if test.worker != "identify permit" {
					_ = readPacket(t, ctx, conn)
					writePacket(t, ctx, conn, `{"op":0,"s":11,"t":"MESSAGE_CREATE","d":{}}`)
				}
				if !waitGatewayBarrier(t, ctx, entered) {
					return
				}
				switch test.stop {
				case "reconnect":
					writePacket(t, ctx, conn, `{"op":7,"d":null}`)
				case "reset":
					writePacket(t, ctx, conn, `{"op":9,"d":false}`)
				case "fatal":
					_ = conn.Close(4004, "private credential detail")
					return
				case "cancel":
					cancel()
				}
				_ = conn.Write(ctx, websocket.MessageText, []byte(`{"op":0,"s":12,"t":"MESSAGE_CREATE","d":{}}`))
				if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
					t.Errorf("gateway did not promptly close after shutdown: err=%v context=%v", err, ctx.Err())
				}
			})
			persisted := resumeCheckpoint(config)
			if test.worker == "identify permit" {
				persisted = nil
				config.BeforeIdentify = blockedWorker
			}
			err := RunShard(ctx, config, persisted, func(ctx context.Context, _ Dispatch, _ Checkpoint) error {
				if test.worker == "identify permit" {
					t.Error("intake started after a failed IDENTIFY permit")
					return workerFailure
				}
				return blockedWorker(ctx)
			})
			if test.worker == "intake panic" {
				require.ErrorContains(t, err, "discord durable intake panicked")
			} else {
				require.ErrorIs(t, err, workerFailure)
			}
			require.NotContains(t, err.Error(), "private")
			if test.stop == "cancel" {
				require.Same(t, workerFailure, err, "independent failure replaces shutdown cancellation")
			} else {
				var stopped *GatewayError
				require.ErrorAs(t, err, &stopped)
				require.Equal(t, test.stop == "reset", stopped.ResetSession)
				require.Equal(t, test.stop == "fatal", stopped.Fatal)
				if test.stop == "fatal" {
					require.Equal(t, 4004, stopped.CloseCode)
				}
			}
			if test.worker == "identify permit" {
				var rateLimit *APIError
				require.ErrorAs(t, err, &rateLimit)
				require.Equal(t, RateLimited, rateLimit.Code)
				require.Equal(t, 17*time.Second, rateLimit.RetryAfter)
			}
			require.EqualValues(t, 1, calls.Load(), "no worker follow-on after shutdown")
			require.EqualValues(t, 1, connections.Load())
			if persisted != nil {
				require.EqualValues(t, 10, persisted.Sequence)
			}
			select {
			case <-exited:
			default:
				t.Fatal("RunShard returned before joining its worker")
			}
		})
	}
}

func TestGatewayReconnectBeforeHello(t *testing.T) {
	config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
		writePacket(t, ctx, conn, `{"op":7,"d":null}`)
		if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
			t.Errorf("pre-HELLO reconnect did not promptly close: %v", err)
		}
	})
	config.BeforeIdentify = func(context.Context) error {
		t.Error("requested an IDENTIFY permit before HELLO")
		return nil
	}
	err := RunShard(t.Context(), config, nil, func(context.Context, Dispatch, Checkpoint) error {
		t.Error("unexpected dispatch before HELLO")
		return nil
	})
	requireGatewayRetry(t, err)
	require.EqualValues(t, 1, connections.Load())
}

func TestGatewayHelloTimeout(t *testing.T) {
	closed := make(chan struct{})
	config, connections := gatewayFixture(t, func(ctx context.Context, conn *websocket.Conn) {
		defer close(closed)
		if _, _, err := conn.Read(ctx); err == nil || ctx.Err() != nil {
			t.Errorf("silent server should see the HELLO deadline close: err=%v context=%v", err, ctx.Err())
		}
	})
	config.helloTimeout = 50 * time.Millisecond
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err := RunShard(ctx, config, nil, func(context.Context, Dispatch, Checkpoint) error {
		t.Error("unexpected dispatch without HELLO")
		return nil
	})
	requireGatewayRetry(t, err)
	require.NoError(t, ctx.Err(), "the HELLO deadline must precede caller cancellation")
	require.True(t, waitGatewayBarrier(t, t.Context(), closed))
	require.EqualValues(t, 1, connections.Load())
}
