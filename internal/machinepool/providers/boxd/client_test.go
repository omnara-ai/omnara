package boxd

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/machinepool/providers/boxd/boxdv1"
)

type fakeServer struct {
	boxdv1.UnimplementedBoxdApiServer

	mu             sync.Mutex
	authorizations []string
	vms            map[string]*boxdv1.GetVmResponse
	created        []*boxdv1.CreateVmRequest
	restored       []*boxdv1.CreateVmFromSnapshotRequest
	destroyed      []string
	execStdin      []byte
	execCommand    string
	execExitCode   int32
	execFail       error
	unauthorized   bool
}

func newFakeServer() *fakeServer {
	return &fakeServer{vms: map[string]*boxdv1.GetVmResponse{}}
}

func (s *fakeServer) record(ctx context.Context) {
	md, _ := metadata.FromIncomingContext(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.authorizations = append(s.authorizations, strings.Join(md.Get("authorization"), ","))
}

// reject returns Unauthenticated when the server has been told to stop
// accepting the current session token.
func (s *fakeServer) reject() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.unauthorized {
		return status.Error(codes.Unauthenticated, "token revoked")
	}
	return nil
}

func (s *fakeServer) lookup(ref string) (*boxdv1.GetVmResponse, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, current := range s.vms {
		if current.GetVmId() == ref || current.GetName() == ref {
			return current, true
		}
	}
	return nil, false
}

//nolint:staticcheck // implements the generated boxdv1.BoxdApiServer method name
func (s *fakeServer) CreateVm(
	ctx context.Context,
	request *boxdv1.CreateVmRequest,
) (*boxdv1.CreateVmResponse, error) {
	s.record(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.created = append(s.created, request)
	created := &boxdv1.GetVmResponse{
		VmId:         "vm-" + request.GetName(),
		Name:         request.GetName(),
		Status:       "pending",
		Vcpu:         request.GetConfig().GetVcpu(),
		MemoryBytes:  request.GetConfig().GetMemoryBytes(),
		AccessDomain: "boxd.sh",
	}
	s.vms[created.GetVmId()] = created
	return &boxdv1.CreateVmResponse{VmId: created.GetVmId(), Name: created.GetName(), Status: "pending"}, nil
}

//nolint:staticcheck // implements the generated boxdv1.BoxdApiServer method name
func (s *fakeServer) CreateVmFromSnapshot(
	ctx context.Context,
	request *boxdv1.CreateVmFromSnapshotRequest,
) (*boxdv1.CreateVmResponse, error) {
	s.record(ctx)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.restored = append(s.restored, request)
	return &boxdv1.CreateVmResponse{VmId: "vm-restored", Name: request.GetName(), Status: "running"}, nil
}

//nolint:staticcheck // implements the generated boxdv1.BoxdApiServer method name
func (s *fakeServer) DestroyVm(
	ctx context.Context,
	request *boxdv1.DestroyVmRequest,
) (*boxdv1.DestroyVmResponse, error) {
	s.record(ctx)
	if _, found := s.lookup(request.GetVmId()); !found {
		return nil, status.Error(codes.NotFound, "VM 'missing' not found")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.destroyed = append(s.destroyed, request.GetVmId())
	return &boxdv1.DestroyVmResponse{}, nil
}

//nolint:staticcheck // implements the generated boxdv1.BoxdApiServer method name
func (s *fakeServer) GetVm(ctx context.Context, request *boxdv1.GetVmRequest) (*boxdv1.GetVmResponse, error) {
	s.record(ctx)
	current, found := s.lookup(request.GetVmId())
	if !found {
		return nil, status.Error(codes.NotFound, "VM not found")
	}
	return current, nil
}

func (s *fakeServer) ListVms(
	ctx context.Context,
	_ *boxdv1.ListVmsRequest,
) (*boxdv1.ListVmsResponse, error) {
	s.record(ctx)
	if err := s.reject(); err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	response := &boxdv1.ListVmsResponse{}
	for _, current := range s.vms {
		response.Vms = append(response.Vms, current)
	}
	return response, nil
}

func (s *fakeServer) Exec(stream grpc.BidiStreamingServer[boxdv1.ExecChunk, boxdv1.ExecChunk]) error {
	s.record(stream.Context())
	first, err := stream.Recv()
	if err != nil {
		return err
	}
	if first.GetCommand() == "" || first.GetVmId() == "" {
		return status.Error(codes.InvalidArgument, "missing command")
	}
	s.mu.Lock()
	s.execCommand = first.GetCommand()
	fail := s.execFail
	exitCode := s.execExitCode
	s.mu.Unlock()
	if fail != nil {
		return fail
	}
	var stdin []byte
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if chunk.GetStdin() {
			stdin = append(stdin, chunk.GetData()...)
		}
	}
	s.mu.Lock()
	s.execStdin = stdin
	s.mu.Unlock()
	if err := stream.Send(&boxdv1.ExecChunk{Data: []byte("read ")}); err != nil {
		return err
	}
	if err := stream.Send(&boxdv1.ExecChunk{Data: []byte("bytes\n")}); err != nil {
		return err
	}
	if err := stream.Send(&boxdv1.ExecChunk{Data: []byte("warning\n"), IsStderr: true}); err != nil {
		return err
	}
	// Fold the exit code into the final data chunk, which boxd's SDKs treat as
	// within contract, so the client must read the code from every chunk.
	return stream.Send(&boxdv1.ExecChunk{Data: []byte("done\n"), ExitCode: exitCode})
}

func (s *fakeServer) GetSnapshot(
	ctx context.Context,
	request *boxdv1.GetSnapshotRequest,
) (*boxdv1.GetSnapshotResponse, error) {
	s.record(ctx)
	if request.GetName() != "team-workspace" {
		return nil, status.Error(codes.NotFound, "snapshot not found")
	}
	return &boxdv1.GetSnapshotResponse{Snapshot: &boxdv1.SnapshotInfo{
		SnapshotId:  "snap-1",
		Name:        "team-workspace",
		Status:      "ready",
		Vcpu:        4,
		MemoryBytes: 16384 * mebibyte,
	}}, nil
}

func (s *fakeServer) GetOrgMachineDefaults(
	ctx context.Context,
	_ *boxdv1.GetOrgMachineDefaultsRequest,
) (*boxdv1.GetOrgMachineDefaultsResponse, error) {
	s.record(ctx)
	return &boxdv1.GetOrgMachineDefaultsResponse{Vcpu: 2, MemoryBytes: 8192 * mebibyte}, nil
}

type exchangeServer struct {
	*httptest.Server
	mu       sync.Mutex
	calls    int
	apiKeys  []string
	status   int
	response map[string]any
}

func newExchangeServer(t *testing.T) *exchangeServer {
	t.Helper()
	exchange := &exchangeServer{
		status:   http.StatusOK,
		response: map[string]any{"token": "jwt-1", "expires_at": time.Now().Add(time.Hour).Unix()},
	}
	exchange.Server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			APIKey string `json:"api_key"`
		}
		if r.Method != http.MethodPost || r.Header.Get("Content-Type") != "application/json" ||
			json.NewDecoder(r.Body).Decode(&body) != nil {
			http.Error(w, "bad exchange request", http.StatusBadRequest)
			return
		}
		exchange.mu.Lock()
		exchange.calls++
		exchange.apiKeys = append(exchange.apiKeys, body.APIKey)
		statusCode, response := exchange.status, exchange.response
		exchange.mu.Unlock()
		if statusCode == http.StatusTooManyRequests {
			w.Header().Set("Retry-After", "7")
		}
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(response)
	}))
	t.Cleanup(exchange.Close)
	return exchange
}

func newTestClient(t *testing.T, server *fakeServer, exchange *exchangeServer) *grpcClient {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	grpcServer := grpc.NewServer()
	boxdv1.RegisterBoxdApiServer(grpcServer, server)
	go func() { _ = grpcServer.Serve(listener) }()
	conn, err := grpc.NewClient(
		"passthrough:///bufconn",
		grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
			return listener.DialContext(ctx)
		}),
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	)
	if err != nil {
		t.Fatalf("dial bufconn: %v", err)
	}
	t.Cleanup(func() {
		_ = conn.Close()
		grpcServer.Stop()
	})
	return &grpcClient{
		apiURL:     "bufconn",
		authURL:    exchange.URL,
		apiKey:     "bxd_test_key",
		httpClient: exchange.Client(),
		stub:       boxdv1.NewBoxdApiClient(conn),
		tokens:     newSessionTokenCache(),
	}
}

func TestBoxdGRPCClientExchangesAPIKeyOnceAndAuthenticatesCalls(t *testing.T) {
	server := newFakeServer()
	exchange := newExchangeServer(t)
	client := newTestClient(t, server, exchange)
	ctx := context.Background()

	created, err := client.CreateVM(ctx, createVMRequest{Name: "omnara-mch-a", VCPU: 2, MemoryBytes: 8192 * mebibyte})
	if err != nil || created.ID != "vm-omnara-mch-a" || created.Name != "omnara-mch-a" || created.Status != "pending" {
		t.Fatalf("create = %+v, error %v", created, err)
	}
	request := server.created[0]
	if request.GetConfig().GetVcpu() != 2 || request.GetConfig().GetMemoryBytes() != 8192*mebibyte ||
		request.GetConfig().GetSrf().GetAutoSuspendTimeoutSecs() != 0 {
		t.Fatalf("create request = %+v", request)
	}
	current, found, err := client.GetVM(ctx, "omnara-mch-a")
	if err != nil || !found || current.ID != "vm-omnara-mch-a" || current.VCPU != 2 ||
		current.MemoryBytes != 8192*mebibyte || current.Status != "pending" ||
		current.url() != "https://omnara-mch-a.boxd.sh/" {
		t.Fatalf("get = %+v found %v error %v", current, found, err)
	}
	if _, found, err := client.GetVM(ctx, "missing"); err != nil || found {
		t.Fatalf("get missing = found %v error %v", found, err)
	}
	vms, err := client.ListVMs(ctx)
	if err != nil || len(vms) != 1 || vms[0].Name != "omnara-mch-a" {
		t.Fatalf("list = %+v, error %v", vms, err)
	}
	restored, err := client.CreateVM(ctx, createVMRequest{Name: "omnara-mch-b", Snapshot: "team-workspace"})
	if err != nil || restored.ID != "vm-restored" || server.restored[0].GetSnapshot() != "team-workspace" ||
		server.restored[0].GetConfig().GetSrf().AutoSuspendTimeoutSecs == nil {
		t.Fatalf("restore = %+v, error %v, request %+v", restored, err, server.restored)
	}
	if _, err := client.CreateVM(ctx, createVMRequest{Name: "x", Snapshot: "s", VCPU: 1}); err == nil {
		t.Fatal("expected sized snapshot restore to be rejected")
	}
	snapshot, found, err := client.GetSnapshot(ctx, "team-workspace")
	if err != nil || !found || snapshot.VCPU != 4 || snapshot.MemoryBytes != 16384*mebibyte || snapshot.Status != "ready" {
		t.Fatalf("snapshot = %+v found %v error %v", snapshot, found, err)
	}
	if _, found, err := client.GetSnapshot(ctx, "other"); err != nil || found {
		t.Fatalf("missing snapshot = found %v error %v", found, err)
	}
	size, err := client.GetOrgMachineDefaults(ctx)
	if err != nil || size.VCPU != 2 || size.MemoryBytes != 8192*mebibyte {
		t.Fatalf("org defaults = %+v, error %v", size, err)
	}
	if err := client.DestroyVM(ctx, "vm-omnara-mch-a"); err != nil || len(server.destroyed) != 1 {
		t.Fatalf("destroy = error %v destroyed %v", err, server.destroyed)
	}
	if err := client.DestroyVM(ctx, "missing"); err != nil {
		t.Fatalf("destroy missing = %v, want nil", err)
	}
	if exchange.calls != 1 || exchange.apiKeys[0] != "bxd_test_key" {
		t.Fatalf("exchange calls = %d keys %v, want one exchange", exchange.calls, exchange.apiKeys)
	}
	for _, authorization := range server.authorizations {
		if authorization != "Bearer jwt-1" {
			t.Fatalf("authorization = %q, want bearer session token", authorization)
		}
	}
}

func TestBoxdGRPCClientExecStreamsStdinAndCollectsOutput(t *testing.T) {
	server := newFakeServer()
	server.vms["vm-1"] = &boxdv1.GetVmResponse{VmId: "vm-1", Name: "omnara-mch-a", Status: "running"}
	client := newTestClient(t, server, newExchangeServer(t))
	payload := []byte(strings.Repeat("x", execStdinChunkBytes*2+17))
	result, err := client.Exec(context.Background(), "vm-1", "sh -c 'cat >/dev/null'", payload)
	if err != nil {
		t.Fatalf("exec: %v", err)
	}
	if result.ExitCode != 0 || result.Stdout != "read bytes\ndone\n" || result.Stderr != "warning\n" {
		t.Fatalf("exec result = %+v", result)
	}
	if server.execCommand != "sh -c 'cat >/dev/null'" || string(server.execStdin) != string(payload) {
		t.Fatalf(
			"server saw command %q and %d stdin bytes, want %d",
			server.execCommand,
			len(server.execStdin),
			len(payload),
		)
	}
	server.execExitCode = 3
	result, err = client.Exec(context.Background(), "vm-1", "false", nil)
	if err != nil || result.ExitCode != 3 {
		t.Fatalf("exit code result = %+v, error %v", result, err)
	}
	server.execFail = status.Error(codes.Unavailable, "cannot connect to VM agent")
	_, err = client.Exec(context.Background(), "vm-1", "true", nil)
	if !isTransient(err) || !isCode(err, codes.Unavailable) || !strings.Contains(err.Error(), "VM agent") {
		t.Fatalf("exec failure = %v, want transient unavailable", err)
	}
}

func TestBoxdGRPCClientRefreshesExpiringSessionTokens(t *testing.T) {
	server := newFakeServer()
	exchange := newExchangeServer(t)
	exchange.response = map[string]any{"token": "jwt-short", "expires_at": time.Now().Add(time.Minute).Unix()}
	client := newTestClient(t, server, exchange)
	if _, err := client.ListVMs(context.Background()); err != nil {
		t.Fatalf("first list: %v", err)
	}
	exchange.mu.Lock()
	exchange.response = map[string]any{"token": "jwt-fresh", "expires_at": time.Now().Add(time.Hour).Unix()}
	exchange.mu.Unlock()
	if _, err := client.ListVMs(context.Background()); err != nil {
		t.Fatalf("second list: %v", err)
	}
	if exchange.calls != 2 || server.authorizations[1] != "Bearer jwt-fresh" {
		t.Fatalf("exchange calls = %d authorizations %v", exchange.calls, server.authorizations)
	}
	if _, err := client.ListVMs(context.Background()); err != nil || exchange.calls != 2 {
		t.Fatalf("third list = error %v exchange calls %d, want cached token", err, exchange.calls)
	}
}

func TestBoxdGRPCClientReportsExchangeFailures(t *testing.T) {
	server := newFakeServer()
	exchange := newExchangeServer(t)
	exchange.status = http.StatusUnauthorized
	client := newTestClient(t, server, exchange)
	_, err := client.ListVMs(context.Background())
	if !isCode(err, codes.Unauthenticated) || !strings.Contains(err.Error(), "HTTP 401") {
		t.Fatalf("unauthorized exchange error = %v", err)
	}
	exchange.status = http.StatusTooManyRequests
	client.tokens = newSessionTokenCache()
	_, err = client.ListVMs(context.Background())
	retryAfter, ok := providers.RetryAfter(err)
	if !isTransient(err) || !ok || retryAfter != 7*time.Second {
		t.Fatalf("rate limited exchange error = %v retry after %v ok %v", err, retryAfter, ok)
	}
	exchange.status = http.StatusOK
	exchange.response = map[string]any{"expires_at": 1}
	client.tokens = newSessionTokenCache()
	if _, err := client.ListVMs(context.Background()); err == nil || !strings.Contains(err.Error(), "missing the token") {
		t.Fatalf("empty token error = %v", err)
	}
	if len(server.authorizations) != 0 {
		t.Fatalf("server saw %d calls without a session token", len(server.authorizations))
	}
}

func TestBoxdErrorClassification(t *testing.T) {
	if fromGRPC(nil) != nil {
		t.Fatal("nil error must stay nil")
	}
	err := fromGRPC(status.Error(codes.NotFound, "VM not found"))
	if !isNotFound(err) || isTransient(err) || err.Error() != "boxd API returned NotFound: VM not found" {
		t.Fatalf("not found = %v", err)
	}
	if err := fromGRPC(errors.New("connection reset")); !isCode(err, codes.Unknown) || !isTransient(err) {
		t.Fatalf("plain error = %v", err)
	}
	for _, code := range []codes.Code{codes.PermissionDenied, codes.InvalidArgument, codes.FailedPrecondition} {
		if isTransient(apiError{Code: code}) {
			t.Fatalf("%s must not be transient", code)
		}
	}
	// ResourceExhausted is a rate limit on reads and the exchange, which is
	// transient, and a full org on a create, which the create path tells apart.
	full := apiError{Code: codes.ResourceExhausted, Message: "Org VM limit reached (100/100)"}
	if !isTransient(full) || !isCapacityRefusal(full) {
		t.Fatalf("resource exhausted = transient %t capacity %t", isTransient(full), isCapacityRefusal(full))
	}
	if isCapacityRefusal(apiError{Code: codes.Unavailable}) {
		t.Fatal("unavailable must not read as a capacity refusal")
	}
	if isTransient(errors.New("not an api error")) {
		t.Fatal("non-API errors must not be transient")
	}
}

func TestBoxdGRPCClientAlwaysSendsAnExplicitIdleWindow(t *testing.T) {
	server := newFakeServer()
	client := newTestClient(t, server, newExchangeServer(t))
	ctx := context.Background()
	if _, err := client.CreateVM(ctx, createVMRequest{Name: "plain", VCPU: 1, MemoryBytes: 4096 * mebibyte}); err != nil {
		t.Fatalf("create without a sleep window: %v", err)
	}
	// Unset would let boxd apply a cluster default or a snapshot's inherited
	// window, so a pool without a sleep window must send an explicit zero.
	srf := server.created[0].GetConfig().GetSrf()
	if srf == nil || srf.AutoSuspendTimeoutSecs == nil || srf.GetAutoSuspendTimeoutSecs() != 0 {
		t.Fatalf("create without a sleep window sent %+v, want an explicit 0", srf)
	}
	if _, err := client.CreateVM(ctx, createVMRequest{
		Name:                   "sleepy",
		VCPU:                   1,
		MemoryBytes:            4096 * mebibyte,
		AutoSuspendTimeoutSecs: 60,
	}); err != nil {
		t.Fatalf("create with a sleep window: %v", err)
	}
	if server.created[1].GetConfig().GetSrf().GetAutoSuspendTimeoutSecs() != 60 {
		t.Fatalf("create with a sleep window sent %+v", server.created[1].GetConfig().GetSrf())
	}
}

func TestBoxdGRPCClientDecodesFractionalExpiryAndCapsTheLifetime(t *testing.T) {
	server := newFakeServer()
	exchange := newExchangeServer(t)
	// boxd's SDKs read expires_at as a float, so a fractional value must decode.
	exchange.response = map[string]any{
		"token":      "jwt-frac",
		"expires_at": float64(time.Now().Add(30*time.Minute).Unix()) + 0.5,
	}
	client := newTestClient(t, server, exchange)
	if _, err := client.ListVMs(context.Background()); err != nil {
		t.Fatalf("list with a fractional expiry: %v", err)
	}
	// A claimed lifetime beyond the documented hour cannot pin a token in the
	// cache: after an hour it must be exchanged again.
	exchange.mu.Lock()
	exchange.response = map[string]any{"token": "jwt-long", "expires_at": time.Now().Add(240 * time.Hour).Unix()}
	exchange.mu.Unlock()
	client.tokens = newSessionTokenCache()
	base := time.Now()
	client.tokens.now = func() time.Time { return base }
	if _, err := client.ListVMs(context.Background()); err != nil {
		t.Fatalf("list with a long expiry: %v", err)
	}
	client.tokens.now = func() time.Time { return base.Add(sessionTokenLifetime) }
	if _, err := client.ListVMs(context.Background()); err != nil {
		t.Fatalf("list after the capped lifetime: %v", err)
	}
	if exchange.calls != 3 {
		t.Fatalf("exchange calls = %d, want a re-exchange after the capped lifetime", exchange.calls)
	}
}

func TestBoxdGRPCClientDropsATokenTheServerStopsAccepting(t *testing.T) {
	server := newFakeServer()
	exchange := newExchangeServer(t)
	client := newTestClient(t, server, exchange)
	ctx := context.Background()
	if _, err := client.ListVMs(ctx); err != nil {
		t.Fatalf("first list: %v", err)
	}
	server.mu.Lock()
	server.unauthorized = true
	server.mu.Unlock()
	exchange.mu.Lock()
	exchange.response = map[string]any{"token": "jwt-rotated", "expires_at": time.Now().Add(time.Hour).Unix()}
	exchange.mu.Unlock()
	if _, err := client.ListVMs(ctx); !isCode(err, codes.Unauthenticated) {
		t.Fatalf("list with a revoked token = %v, want unauthenticated", err)
	}
	// The rejected token is gone from the cache, so the next call exchanges
	// again instead of failing until the recorded expiry.
	server.mu.Lock()
	server.unauthorized = false
	server.mu.Unlock()
	if _, err := client.ListVMs(ctx); err != nil {
		t.Fatalf("list after rotation: %v", err)
	}
	last := server.authorizations[len(server.authorizations)-1]
	if exchange.calls != 2 || last != "Bearer jwt-rotated" {
		t.Fatalf("exchange calls = %d, last authorization = %q", exchange.calls, last)
	}
}

func TestBoxdGRPCClientExchangesOnceUnderConcurrency(t *testing.T) {
	server := newFakeServer()
	exchange := newExchangeServer(t)
	client := newTestClient(t, server, exchange)
	const callers = 8
	var wg sync.WaitGroup
	errs := make(chan error, callers)
	for range callers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := client.ListVMs(context.Background())
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent list: %v", err)
		}
	}
	if exchange.calls != 1 {
		t.Fatalf("exchange calls = %d, want one exchange shared by %d callers", exchange.calls, callers)
	}
}

func TestBoxdExchangeStatusMapping(t *testing.T) {
	for statusCode, want := range map[int]codes.Code{
		http.StatusUnauthorized:        codes.Unauthenticated,
		http.StatusForbidden:           codes.PermissionDenied,
		http.StatusNotFound:            codes.NotFound,
		http.StatusTooManyRequests:     codes.ResourceExhausted,
		http.StatusBadRequest:          codes.InvalidArgument,
		http.StatusUnprocessableEntity: codes.InvalidArgument,
		http.StatusInternalServerError: codes.Unavailable,
		http.StatusBadGateway:          codes.Unavailable,
	} {
		if got := codeForHTTPStatus(statusCode); got != want {
			t.Fatalf("status %d = %s, want %s", statusCode, got, want)
		}
	}
	// A rejected key is a configuration error, not something to retry.
	if isTransient(apiError{Code: codeForHTTPStatus(http.StatusBadRequest)}) {
		t.Fatal("a 400 from the exchange must not be transient")
	}
}
