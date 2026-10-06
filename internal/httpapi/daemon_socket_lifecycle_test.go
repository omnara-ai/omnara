package httpapi

import (
	"bufio"
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
	logpkg "github.com/omnara-ai/omnara/internal/log"
	"github.com/omnara-ai/omnara/internal/notifications"
)

func TestDaemonSocketHubShutdownWaitsForHandlers(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	hub := &daemonSocketHub{}
	first, finishFirst, ok := hub.beginHandler(ctx)
	if !ok {
		t.Fatal("first handler rejected")
	}
	finishFirst = sync.OnceFunc(finishFirst)
	defer finishFirst()
	second, finishSecond, ok := hub.beginHandler(ctx)
	if !ok {
		t.Fatal("second handler rejected")
	}
	finishSecond = sync.OnceFunc(finishSecond)
	defer finishSecond()
	closed := make(chan struct{})
	go func() { hub.Close(); close(closed) }()
	for _, handlerCtx := range []context.Context{first, second} {
		waitDaemonSocketLifecycle(t, ctx, handlerCtx.Done())
		if !errors.Is(context.Cause(handlerCtx), logpkg.ErrSocketClosed) {
			t.Fatalf("shutdown cause = %v", context.Cause(handlerCtx))
		}
	}
	if _, _, ok := hub.beginHandler(ctx); ok {
		t.Fatal("handler admitted after shutdown started")
	}
	finishFirst()
	select {
	case <-closed:
		t.Fatal("shutdown returned before second handler finished cleanup")
	default:
	}
	finishSecond()
	waitDaemonSocketLifecycle(t, ctx, closed)
	hub.Close()
}

func TestDaemonSocketHubRegistrationDuringShutdown(t *testing.T) {
	for _, order := range []string{"before", "after", "concurrent"} {
		t.Run(order, func(t *testing.T) {
			hub := &daemonSocketHub{
				byMachine: map[uuid.UUID]*daemonSocket{},
				byRuntime: map[uuid.UUID]*daemonSocket{},
			}
			socket := newTestDaemonSocket(t, &daemonSocket{machineID: uuid.New(), runtimeID: uuid.New()})
			switch order {
			case "before":
				if !hub.register(socket) {
					t.Fatal("registration rejected before shutdown")
				}
				hub.Close()
			case "after":
				hub.Close()
				if hub.register(socket) {
					t.Fatal("registration accepted after shutdown")
				}
			case "concurrent":
				start := make(chan struct{})
				var workers sync.WaitGroup
				workers.Go(func() { <-start; hub.register(socket) })
				workers.Go(func() { <-start; hub.Close() })
				close(start)
				workers.Wait()
			}
			select {
			case <-socket.done:
			default:
				t.Fatal("shutdown missed socket")
			}
		})
	}
}

type stalledSocketUpgradeWriter struct {
	*httptest.ResponseRecorder
	started, interrupted chan struct{}
	once                 sync.Once
}

func (w *stalledSocketUpgradeWriter) SetWriteDeadline(deadline time.Time) error {
	if !deadline.IsZero() && !deadline.After(time.Now()) {
		w.once.Do(func() { close(w.interrupted) })
	}
	return nil
}

func (w *stalledSocketUpgradeWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	close(w.started)
	<-w.interrupted
	return nil, nil, os.ErrDeadlineExceeded
}

func TestDaemonSocketShutdownDuringUpgrade(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	writer := &stalledSocketUpgradeWriter{
		ResponseRecorder: httptest.NewRecorder(),
		started:          make(chan struct{}), interrupted: make(chan struct{}),
	}
	defer writer.once.Do(func() { close(writer.interrupted) })
	request := httptest.NewRequestWithContext(ctx, http.MethodGet, "/socket", nil)
	request.Header.Set("Connection", "Upgrade")
	request.Header.Set("Upgrade", "websocket")
	request.Header.Set("Sec-WebSocket-Version", "13")
	request.Header.Set("Sec-WebSocket-Key", "dGhlIHNhbXBsZSBub25jZQ==")
	backend := &Server{daemonHub: &daemonSocketHub{}}
	finished := make(chan struct{})
	go func() {
		defer close(finished)
		backend.socketMachineDaemonRuntime(writer, request, machineDaemonScope{}, uuid.New())
	}()
	waitDaemonSocketLifecycle(t, ctx, writer.started)
	closed := make(chan struct{})
	go func() { backend.Close(); close(closed) }()
	waitDaemonSocketLifecycle(t, ctx, closed)
	waitDaemonSocketLifecycle(t, ctx, finished)
}

// These operations simulate Redis accepting a write before cancellation or a
// failed response. Cleanup must cover both keys even when setup returns an error.
type socketSetupPresence struct {
	notifications.DaemonPresenceStore
	setupStarted   chan context.Context
	cleanupStarted chan context.Context
	releaseCleanup chan struct{}
	waitForCancel  bool
	mu             sync.Mutex
	machineOwner   notifications.PresenceOwner
	runtimeOwner   notifications.PresenceOwner
}

func (p *socketSetupPresence) PutIfRuntime(
	ctx context.Context, _ uuid.UUID, presence notifications.DaemonPresence, _ time.Duration,
) error {
	p.mu.Lock()
	p.machineOwner = presence.PresenceOwner
	p.mu.Unlock()
	p.setupStarted <- ctx
	if p.waitForCancel {
		<-ctx.Done()
		return ctx.Err()
	}
	return nil
}

func (p *socketSetupPresence) PutRuntime(
	_ context.Context, _ uuid.UUID, presence notifications.DaemonPresence, _ time.Duration,
) error {
	p.mu.Lock()
	p.runtimeOwner = presence.PresenceOwner
	p.mu.Unlock()
	return errors.New("Redis response lost after write")
}

func (p *socketSetupPresence) DeleteIfOwned(ctx context.Context, _ uuid.UUID, owner notifications.PresenceOwner) error {
	p.cleanupStarted <- ctx
	select {
	case <-p.releaseCleanup:
	case <-ctx.Done():
		return ctx.Err()
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.machineOwner == owner {
		p.machineOwner = notifications.PresenceOwner{}
	}
	return nil
}

func (p *socketSetupPresence) DeleteRuntimeIfOwned(
	_ context.Context, _ uuid.UUID, owner notifications.PresenceOwner,
) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.runtimeOwner == owner {
		p.runtimeOwner = notifications.PresenceOwner{}
	}
	return nil
}

func TestDaemonSocketSetupCleanup(t *testing.T) {
	for _, shutdown := range []bool{false, true} {
		name := "partial presence write"
		if shutdown {
			name = "shutdown during setup"
		}
		t.Run(name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
			defer cancel()
			presence := &socketSetupPresence{
				setupStarted: make(chan context.Context, 1), cleanupStarted: make(chan context.Context, 1),
				releaseCleanup: make(chan struct{}), waitForCancel: shutdown,
			}
			releaseCleanup := sync.OnceFunc(func() { close(presence.releaseCleanup) })
			defer releaseCleanup()
			hub := &daemonSocketHub{presence: presence, replicaID: uuid.New()}
			backend := &Server{daemonHub: hub}
			finished := make(chan struct{}, 1)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				defer func() { finished <- struct{}{} }()
				backend.socketMachineDaemonRuntime(w, r, machineDaemonScope{
					OrgID: uuid.New(), MachineID: uuid.New(), DaemonTokenID: uuid.New(),
				}, uuid.New())
			}))
			defer server.Close()
			conn, response, err := websocket.Dial(ctx, server.URL, nil)
			if response != nil && response.Body != nil {
				defer response.Body.Close()
			}
			if err != nil {
				t.Fatal(err)
			}
			defer conn.CloseNow()
			peerClosed := make(chan struct{})
			go func() { _, _, _ = conn.Read(ctx); close(peerClosed) }()
			var setupCtx context.Context
			select {
			case setupCtx = <-presence.setupStarted:
			case <-ctx.Done():
				t.Fatal("setup did not start")
			}
			assertDaemonSocketDeadline(t, setupCtx, 10*time.Second)
			closed := make(chan struct{})
			if shutdown {
				go func() { backend.Close(); close(closed) }()
				waitDaemonSocketLifecycle(t, ctx, setupCtx.Done())
				if !errors.Is(context.Cause(setupCtx), logpkg.ErrSocketClosed) {
					t.Fatalf("setup cancellation = %v", context.Cause(setupCtx))
				}
			}
			select {
			case cleanupCtx := <-presence.cleanupStarted:
				if cleanupCtx.Err() != nil {
					t.Fatalf("cleanup inherited cancellation: %v", cleanupCtx.Err())
				}
				assertDaemonSocketDeadline(t, cleanupCtx, 5*time.Second)
			case <-ctx.Done():
				t.Fatal("cleanup did not start")
			}
			if shutdown {
				select {
				case <-closed:
					t.Fatal("shutdown returned before presence cleanup")
				default:
				}
				request, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
				if err != nil {
					t.Fatal(err)
				}
				rejected, err := http.DefaultClient.Do(request)
				if err != nil {
					t.Fatal(err)
				}
				defer rejected.Body.Close()
				if rejected.StatusCode != http.StatusServiceUnavailable {
					t.Fatalf("request during shutdown returned %d", rejected.StatusCode)
				}
			}
			releaseCleanup()
			if shutdown {
				waitDaemonSocketLifecycle(t, ctx, closed)
			}
			waitDaemonSocketLifecycle(t, ctx, peerClosed)
			waitDaemonSocketLifecycle(t, ctx, finished)
			presence.mu.Lock()
			defer presence.mu.Unlock()
			if presence.machineOwner != (notifications.PresenceOwner{}) ||
				presence.runtimeOwner != (notifications.PresenceOwner{}) {
				t.Fatal("setup left owned presence records behind")
			}
		})
	}
}

func waitDaemonSocketLifecycle(t *testing.T, ctx context.Context, done <-chan struct{}) {
	t.Helper()
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("socket lifecycle operation did not finish: ", ctx.Err())
	}
}

type socketUpgradeBarrierListener struct {
	net.Listener
	release <-chan struct{}
}

func (l socketUpgradeBarrierListener) Accept() (net.Conn, error) {
	conn, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	return socketUpgradeBarrierConn{Conn: conn, release: l.release}, nil
}

type socketUpgradeBarrierConn struct {
	net.Conn
	release <-chan struct{}
}

func (c socketUpgradeBarrierConn) Write(p []byte) (int, error) {
	n, err := c.Conn.Write(p)
	if err == nil {
		// The client sees HTTP 101 while the server is still in Hijack,
		// before net/http stops watching the connection for disconnects.
		<-c.release
	}
	return n, err
}

func TestDaemonSocketDisconnectDuringUpgradeCleansUp(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	presence := &socketSetupPresence{
		setupStarted: make(chan context.Context, 1), cleanupStarted: make(chan context.Context, 1),
		releaseCleanup: make(chan struct{}), waitForCancel: true,
	}
	releaseCleanup := sync.OnceFunc(func() { close(presence.releaseCleanup) })
	defer releaseCleanup()
	backend := &Server{daemonHub: &daemonSocketHub{presence: presence, replicaID: uuid.New()}}
	release := make(chan struct{})
	releaseUpgrade := sync.OnceFunc(func() { close(release) })
	defer releaseUpgrade()
	stopRelease := context.AfterFunc(ctx, releaseUpgrade)
	defer stopRelease()
	finished := make(chan struct{})
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer close(finished)
		stop := context.AfterFunc(r.Context(), releaseUpgrade)
		defer stop()
		backend.socketMachineDaemonRuntime(w, r, machineDaemonScope{
			OrgID: uuid.New(), MachineID: uuid.New(), DaemonTokenID: uuid.New(),
		}, uuid.New())
	}))
	server.Listener = socketUpgradeBarrierListener{Listener: server.Listener, release: release}
	server.Start()
	defer server.Close()
	conn, response, err := websocket.Dial(ctx, server.URL, nil)
	if response != nil && response.Body != nil {
		defer response.Body.Close()
	}
	if err != nil {
		t.Fatal(err)
	}
	defer conn.CloseNow()
	if err := conn.CloseNow(); err != nil {
		t.Fatal(err)
	}
	select {
	case setupCtx := <-presence.setupStarted:
		if !errors.Is(setupCtx.Err(), context.Canceled) {
			t.Fatalf("upgrade did not observe HTTP cancellation: %v", setupCtx.Err())
		}
	case <-ctx.Done():
		t.Fatal("setup did not exit after early disconnect")
	}
	select {
	case cleanupCtx := <-presence.cleanupStarted:
		if cleanupCtx.Err() != nil {
			t.Fatalf("cleanup inherited HTTP cancellation: %v", cleanupCtx.Err())
		}
		assertDaemonSocketDeadline(t, cleanupCtx, 5*time.Second)
	case <-ctx.Done():
		t.Fatal("early disconnect skipped cleanup")
	}
	releaseCleanup()
	waitDaemonSocketLifecycle(t, ctx, finished)
	backend.Close()
	presence.mu.Lock()
	defer presence.mu.Unlock()
	if presence.machineOwner != (notifications.PresenceOwner{}) {
		t.Fatal("early disconnect left a presence record")
	}
}

func assertDaemonSocketDeadline(t *testing.T, ctx context.Context, limit time.Duration) {
	t.Helper()
	deadline, ok := ctx.Deadline()
	if !ok || time.Until(deadline) > limit {
		t.Fatalf("operation has no deadline within %s", limit)
	}
}
