package machinedaemon

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/omnara-ai/omnara/internal/machinedaemon/localipc"
)

// A process service is a local HTTP service on a unix socket in the run
// directory that processes the daemon starts call with a capability from
// their environment: an HMAC of the process ID under its supervisor token,
// labeled per service so one service's capability is useless at another.
type processService struct {
	name   string
	label  string
	socket string
}

var (
	gitCredentialService = processService{
		name: "Git credential", label: "omnara-git-credential", socket: "git-credentials.sock"}
	programToolCallService = processService{
		name: "program tool call", label: "omnara-program-tool-call", socket: "tool-calls.sock"}
)

// processServiceHelper is how a process reaches a service: the omnarad
// executable, the service's socket, and the process's capability.
type processServiceHelper struct {
	Executable string `json:"executable"`
	Endpoint   string `json:"endpoint"`
	Capability string `json:"capability"`
}

func (s processService) capability(supervisorToken, processID string) string {
	mac := hmac.New(sha256.New, []byte(supervisorToken))
	_, _ = mac.Write([]byte(s.label + "\x00" + processID))
	return hex.EncodeToString(mac.Sum(nil))
}

func (c *Client) processServiceEndpoint(s processService) (string, error) {
	machine, err := c.machineStore()
	if err != nil {
		return "", err
	}
	return filepath.Join(machine.RunDir(), s.socket), nil
}

func (c *Client) processServiceHelper(
	s processService, supervisorToken, processID string,
) (*processServiceHelper, error) {
	endpoint, err := c.processServiceEndpoint(s)
	if err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err == nil {
		executable, err = filepath.Abs(executable)
	}
	if err != nil {
		return nil, err
	}
	return &processServiceHelper{Executable: executable, Endpoint: endpoint,
		Capability: s.capability(supervisorToken, processID)}, nil
}

// authorize returns 0 when r carries processID's capability for s and the
// process is open locally, 403 when it does not, and 503 when the daemon's
// state cannot be read.
func (c *Client) authorizeProcessService(
	ctx context.Context, s processService, r *http.Request, processID string,
) int {
	state, err := c.stateStore(ctx)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	process, found, err := state.Process(ctx, processID)
	if err != nil {
		return http.StatusServiceUnavailable
	}
	capability, bearer := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	want := s.capability(process.SupervisorToken, processID)
	if !found || process.LocalClosed || !bearer || !hmac.Equal([]byte(capability), []byte(want)) {
		return http.StatusForbidden
	}
	return 0
}

// serveProcessService serves server on s's socket until the returned stop.
func (c *Client) serveProcessService(ctx context.Context, s processService, server *http.Server) (func(), error) {
	endpoint, err := c.processServiceEndpoint(s)
	if err != nil {
		return nil, err
	}
	if err := localipc.Cleanup(endpoint); err != nil {
		return nil, err
	}
	listener, err := localipc.Listen(ctx, endpoint)
	if err != nil {
		return nil, err
	}
	serverCtx, cancel := context.WithCancel(ctx)
	server.BaseContext = func(net.Listener) context.Context { return serverCtx }
	done := make(chan struct{})
	go func() {
		defer close(done)
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			c.log.Error(s.name+" service stopped", "error", err)
		}
	}()
	return func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer shutdownCancel()
		if err := server.Shutdown(shutdownCtx); err != nil {
			_ = server.Close()
		}
		<-done
		_ = localipc.Cleanup(endpoint)
	}, nil
}

// dialProcessService connects to a process service, retrying briefly while
// the daemon (re)starts it.
func dialProcessService(endpoint, unavailable string) *http.Transport {
	return &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			connectCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			for {
				conn, err := localipc.Dial(connectCtx, endpoint)
				if err == nil {
					return conn, nil
				}
				if err := sleepContext(connectCtx, 250*time.Millisecond); err != nil {
					return nil, errors.New(unavailable)
				}
			}
		},
	}
}

func noRedirects(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
