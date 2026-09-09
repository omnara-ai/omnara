package boxd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"sync"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"

	"github.com/omnara-ai/omnara/internal/machinepool/providers"
	"github.com/omnara-ai/omnara/internal/machinepool/providers/boxd/boxdv1"
	"github.com/omnara-ai/omnara/internal/ssrf"
)

const (
	// Session tokens exchanged from an API key live for one hour. Refreshing
	// early keeps an in-flight call from presenting an expired token.
	sessionTokenLifetime    = time.Hour
	sessionTokenRefreshSkew = 5 * time.Minute
	dialTimeout             = 10 * time.Second
	execStdinChunkBytes     = 32 * 1024
	maxExecOutputBytes      = 16 * 1024
)

type apiClient interface {
	GetVM(context.Context, string) (vm, bool, error)
	ListVMs(context.Context) ([]vm, error)
	CreateVM(context.Context, createVMRequest) (vm, error)
	DestroyVM(context.Context, string) error
	// Exec runs a shell command in the machine with stdin fed from the
	// payload and returns once the command exits.
	Exec(context.Context, string, string, []byte) (execResult, error)
	GetSnapshot(context.Context, string) (snapshotInfo, bool, error)
	GetOrgMachineDefaults(context.Context) (machineSize, error)
}

type vmStatus string

type vm struct {
	ID          string
	Name        string
	Status      vmStatus
	VCPU        int
	MemoryBytes uint64
	// AccessDomain is the zone the machine is addressed under, which is the
	// org's custom domain when it has one and the cluster zone otherwise.
	AccessDomain string
}

// url is the machine's public HTTPS address. Omnara stores it as the machine's
// sandbox url, which is also the marker it uses to decide a machine can be
// woken, so a machine without one is never allowed to sleep.
func (v vm) url() string {
	if v.Name == "" || v.AccessDomain == "" {
		return ""
	}
	return "https://" + v.Name + "." + v.AccessDomain + "/"
}

type createVMRequest struct {
	Name string
	// Snapshot restores the machine from a boxd snapshot; the restore is born
	// at the snapshot's size, so VCPU and MemoryBytes must be zero with it.
	Snapshot    string
	VCPU        int
	MemoryBytes uint64
	// AutoSuspendTimeoutSecs is boxd's idle window; zero disables it.
	AutoSuspendTimeoutSecs uint32
}

type execResult struct {
	ExitCode int
	Stdout   string
	Stderr   string
}

type snapshotInfo struct {
	ID          string
	Name        string
	Status      string
	VCPU        int
	MemoryBytes uint64
}

type machineSize struct {
	VCPU        int
	MemoryBytes uint64
}

type apiError struct {
	Code    codes.Code
	Message string
	cause   error
}

func (e apiError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("boxd API returned %s", e.Code)
	}
	return fmt.Sprintf("boxd API returned %s: %s", e.Code, e.Message)
}

func (e apiError) Unwrap() error {
	return e.cause
}

func fromGRPC(err error) error {
	if err == nil {
		return nil
	}
	if grpcStatus, ok := status.FromError(err); ok {
		return apiError{Code: grpcStatus.Code(), Message: grpcStatus.Message(), cause: err}
	}
	return apiError{Code: codes.Unknown, Message: err.Error(), cause: err}
}

func isCode(err error, code codes.Code) bool {
	var apiErr apiError
	return errors.As(err, &apiErr) && apiErr.Code == code
}

func isNotFound(err error) bool {
	return isCode(err, codes.NotFound)
}

// isTransient reports whether a provider error describes a condition that an
// immediate retry may clear rather than a rejected request. ResourceExhausted
// is included because on reads and the token exchange it is a rate limit that
// clears in seconds; a create that is refused for lack of room is told apart
// by isCapacityRefusal, since the same code then means something that only
// clears when someone frees a machine.
func isTransient(err error) bool {
	var apiErr apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case codes.Unavailable,
		codes.ResourceExhausted,
		codes.DeadlineExceeded,
		codes.Aborted,
		codes.Internal,
		codes.Unknown:
		return true
	default:
		return false
	}
}

// isRejectedRequest reports whether boxd answered a request by refusing it
// outright, which it does synchronously with a terminal code. Such an answer
// proves the request had no effect; a transport failure proves nothing.
func isRejectedRequest(err error) bool {
	var apiErr apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case codes.InvalidArgument,
		codes.FailedPrecondition,
		codes.PermissionDenied,
		codes.Unauthenticated,
		codes.NotFound,
		codes.AlreadyExists,
		codes.OutOfRange,
		codes.Unimplemented:
		return true
	default:
		return false
	}
}

// isCapacityRefusal reports whether boxd refused to create a machine for lack
// of room. boxd answers ResourceExhausted for its org machine limit and quota
// ceiling, and those resolve on the provider's schedule, not ours, so a
// create that sees this must surface boxd's own message and leave the retry
// to the slower reconciliation backoff rather than the immediate retry loop.
func isCapacityRefusal(err error) bool {
	return isCode(err, codes.ResourceExhausted)
}

// ErrCapacityRefused wraps a capacity refusal so callers can tell it apart
// from a rejected request without inspecting gRPC codes.
var ErrCapacityRefused = errors.New("boxd refused the machine for lack of capacity")

type grpcClient struct {
	apiURL     string
	authURL    string
	apiKey     string
	httpClient *http.Client
	// stub bypasses dialing when set and is used by tests.
	stub   boxdv1.BoxdApiClient
	tokens *sessionTokenCache
}

func newGRPCClient(apiURL, authURL, apiKey string) *grpcClient {
	return &grpcClient{
		apiURL:     apiURL,
		authURL:    authURL,
		apiKey:     apiKey,
		httpClient: providers.NewHTTPClient(),
		tokens:     sharedSessionTokens,
	}
}

func (c *grpcClient) api() (boxdv1.BoxdApiClient, error) {
	if c.stub != nil {
		return c.stub, nil
	}
	conn, err := sharedConnections.get(c.apiURL)
	if err != nil {
		return nil, err
	}
	return boxdv1.NewBoxdApiClient(conn), nil
}

// session returns the API client and a context carrying a session token.
// Callers pass every RPC error through the returned finish func so a token
// boxd has stopped accepting is dropped from the cache.
func (c *grpcClient) session(
	ctx context.Context,
) (boxdv1.BoxdApiClient, context.Context, func(error) error, error) {
	api, err := c.api()
	if err != nil {
		return nil, nil, nil, err
	}
	token, err := c.tokens.token(ctx, c)
	if err != nil {
		return nil, nil, nil, err
	}
	finish := func(err error) error {
		err = fromGRPC(err)
		if isCode(err, codes.Unauthenticated) {
			c.tokens.invalidate(c, token)
		}
		return err
	}
	return api, metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token), finish, nil
}

func (c *grpcClient) GetVM(ctx context.Context, ref string) (vm, bool, error) {
	api, ctx, finish, err := c.session(ctx)
	if err != nil {
		return vm{}, false, err
	}
	response, err := api.GetVm(ctx, &boxdv1.GetVmRequest{VmId: ref})
	if err != nil {
		err = finish(err)
		if isNotFound(err) {
			return vm{}, false, nil
		}
		return vm{}, false, err
	}
	return vmFromResponse(response), true, nil
}

func (c *grpcClient) ListVMs(ctx context.Context) ([]vm, error) {
	api, ctx, finish, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	response, err := api.ListVms(ctx, &boxdv1.ListVmsRequest{})
	if err != nil {
		return nil, finish(err)
	}
	vms := make([]vm, 0, len(response.GetVms()))
	for _, item := range response.GetVms() {
		vms = append(vms, vmFromResponse(item))
	}
	return vms, nil
}

func (c *grpcClient) CreateVM(ctx context.Context, request createVMRequest) (vm, error) {
	api, ctx, finish, err := c.session(ctx)
	if err != nil {
		return vm{}, err
	}
	// The idle window is always sent explicitly. Left unset, boxd falls back to
	// a cluster default, and a snapshot restore inherits its source's window,
	// so a pool without a sleep window could still get a machine that suspends
	// once its daemon dies. Such a machine observes as inactive, which runtime
	// protection reads as legitimately asleep and never reclaims. Daytona sends
	// AutoStopInterval 0 for the same reason.
	config := &boxdv1.VmConfig{
		Srf: &boxdv1.SrfConfig{AutoSuspendTimeoutSecs: proto.Uint32(request.AutoSuspendTimeoutSecs)},
	}
	var response *boxdv1.CreateVmResponse
	if request.Snapshot != "" {
		if request.VCPU != 0 || request.MemoryBytes != 0 {
			return vm{}, errors.New("boxd snapshot restores cannot request a machine size")
		}
		response, err = api.CreateVmFromSnapshot(ctx, &boxdv1.CreateVmFromSnapshotRequest{
			Snapshot: request.Snapshot,
			Name:     request.Name,
			Config:   config,
		})
	} else {
		if request.VCPU < 0 {
			return vm{}, errors.New("boxd machine vcpu must not be negative")
		}
		config.Vcpu = uint32(request.VCPU)
		config.MemoryBytes = request.MemoryBytes
		response, err = api.CreateVm(ctx, &boxdv1.CreateVmRequest{
			Name:   request.Name,
			Config: config,
		})
	}
	if err != nil {
		return vm{}, finish(err)
	}
	return vm{
		ID:     response.GetVmId(),
		Name:   response.GetName(),
		Status: vmStatus(response.GetStatus()),
	}, nil
}

func (c *grpcClient) DestroyVM(ctx context.Context, ref string) error {
	api, ctx, finish, err := c.session(ctx)
	if err != nil {
		return err
	}
	if _, err := api.DestroyVm(ctx, &boxdv1.DestroyVmRequest{VmId: ref}); err != nil {
		err = finish(err)
		if isNotFound(err) {
			return nil
		}
		return err
	}
	return nil
}

func (c *grpcClient) Exec(
	ctx context.Context,
	ref string,
	command string,
	stdin []byte,
) (execResult, error) {
	api, ctx, finish, err := c.session(ctx)
	if err != nil {
		return execResult{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := api.Exec(ctx)
	if err != nil {
		return execResult{}, finish(err)
	}
	if err := sendExecInput(stream, ref, command, stdin); err != nil {
		return execResult{}, finish(err)
	}
	// Every chunk may carry the exit code and the last one is authoritative,
	// which is the convention boxd's own SDKs follow. The server closes the
	// stream with an error rather than a clean EOF when the guest never
	// reported an exit, so a clean EOF is a completed command.
	var result execResult
	var stdout, stderr []byte
	received := false
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return execResult{}, finish(err)
		}
		received = true
		result.ExitCode = int(chunk.GetExitCode())
		if chunk.GetIsStderr() {
			stderr = appendBounded(stderr, chunk.GetData())
		} else {
			stdout = appendBounded(stdout, chunk.GetData())
		}
	}
	if !received {
		return execResult{}, errors.New("boxd exec ended without a response")
	}
	result.Stdout = string(stdout)
	result.Stderr = string(stderr)
	return result, nil
}

func sendExecInput(
	stream grpc.BidiStreamingClient[boxdv1.ExecChunk, boxdv1.ExecChunk],
	ref, command string,
	stdin []byte,
) error {
	chunks := []*boxdv1.ExecChunk{{VmId: ref, Command: command}}
	for len(stdin) > 0 {
		size := min(len(stdin), execStdinChunkBytes)
		chunks = append(chunks, &boxdv1.ExecChunk{Stdin: true, Data: stdin[:size]})
		stdin = stdin[size:]
	}
	for _, chunk := range chunks {
		if err := stream.Send(chunk); err != nil {
			// The server closed the stream early; Recv surfaces its status.
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	return stream.CloseSend()
}

func appendBounded(buffer, data []byte) []byte {
	remaining := maxExecOutputBytes - len(buffer)
	if remaining <= 0 {
		return buffer
	}
	return append(buffer, data[:min(len(data), remaining)]...)
}

func (c *grpcClient) GetSnapshot(ctx context.Context, name string) (snapshotInfo, bool, error) {
	api, ctx, finish, err := c.session(ctx)
	if err != nil {
		return snapshotInfo{}, false, err
	}
	response, err := api.GetSnapshot(ctx, &boxdv1.GetSnapshotRequest{Name: name})
	if err != nil {
		err = finish(err)
		if isNotFound(err) {
			return snapshotInfo{}, false, nil
		}
		return snapshotInfo{}, false, err
	}
	snapshot := response.GetSnapshot()
	if snapshot == nil {
		return snapshotInfo{}, false, errors.New("boxd snapshot response is missing the snapshot")
	}
	return snapshotInfo{
		ID:          snapshot.GetSnapshotId(),
		Name:        snapshot.GetName(),
		Status:      snapshot.GetStatus(),
		VCPU:        int(snapshot.GetVcpu()),
		MemoryBytes: snapshot.GetMemoryBytes(),
	}, true, nil
}

func (c *grpcClient) GetOrgMachineDefaults(ctx context.Context) (machineSize, error) {
	api, ctx, finish, err := c.session(ctx)
	if err != nil {
		return machineSize{}, err
	}
	response, err := api.GetOrgMachineDefaults(ctx, &boxdv1.GetOrgMachineDefaultsRequest{})
	if err != nil {
		return machineSize{}, finish(err)
	}
	return machineSize{VCPU: int(response.GetVcpu()), MemoryBytes: response.GetMemoryBytes()}, nil
}

func vmFromResponse(response *boxdv1.GetVmResponse) vm {
	return vm{
		ID:           response.GetVmId(),
		Name:         response.GetName(),
		Status:       vmStatus(response.GetStatus()),
		VCPU:         int(response.GetVcpu()),
		MemoryBytes:  response.GetMemoryBytes(),
		AccessDomain: response.GetAccessDomain(),
	}
}

// exchangeAPIKey trades the pool's API key for a short-lived session token as
// documented at https://boxd.sh/reference/grpc-api#authentication.
func (c *grpcClient) exchangeAPIKey(ctx context.Context) (sessionToken, error) {
	response, err := providers.DoHTTPResponse(
		ctx,
		c.httpClient,
		providers.Boxd,
		http.MethodPost,
		c.authURL,
		nil,
		map[string]string{"api_key": c.apiKey},
	)
	if err != nil {
		return sessionToken{}, err
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return sessionToken{}, providers.WithRetryAfter(
			apiError{
				Code:    codeForHTTPStatus(response.StatusCode),
				Message: fmt.Sprintf("token exchange returned HTTP %d", response.StatusCode),
			},
			response.Header,
		)
	}
	// expires_at is unix seconds and boxd's SDKs read it as a float, so a
	// fractional value must decode.
	var body struct {
		Token     string  `json:"token"`
		ExpiresAt float64 `json:"expires_at"`
	}
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return sessionToken{}, fmt.Errorf("decode boxd token exchange response: %w", err)
	}
	if body.Token == "" {
		return sessionToken{}, errors.New("boxd token exchange response is missing the token")
	}
	now := c.tokens.now()
	// The documented lifetime caps what the response claims, so a wrong unit
	// or clock cannot pin a dead token in the cache.
	expiresAt := now.Add(sessionTokenLifetime)
	if body.ExpiresAt > 0 {
		seconds, fraction := math.Modf(body.ExpiresAt)
		claimed := time.Unix(int64(seconds), int64(fraction*float64(time.Second)))
		if claimed.Before(expiresAt) {
			expiresAt = claimed
		}
	}
	return sessionToken{value: body.Token, expiresAt: expiresAt}, nil
}

// codeForHTTPStatus maps a token exchange status onto the gRPC code the rest
// of the client reasons about. A rejected request is permanent, so a client
// error other than rate limiting must not read as transient.
func codeForHTTPStatus(statusCode int) codes.Code {
	switch {
	case statusCode == http.StatusUnauthorized:
		return codes.Unauthenticated
	case statusCode == http.StatusForbidden:
		return codes.PermissionDenied
	case statusCode == http.StatusNotFound:
		return codes.NotFound
	case statusCode == http.StatusTooManyRequests:
		return codes.ResourceExhausted
	case statusCode >= http.StatusInternalServerError:
		return codes.Unavailable
	case statusCode >= http.StatusBadRequest:
		return codes.InvalidArgument
	default:
		return codes.Unknown
	}
}

type sessionToken struct {
	value     string
	expiresAt time.Time
}

// sessionTokenCache shares exchanged session tokens between the short-lived
// provider instances the manager builds per operation, keeping runtime
// reconciliation from hitting the per-IP rate limit on the exchange endpoint.
// Exchanges for one key are single-flight for the same reason: eight
// reconcilers waking a cold cache must produce one exchange, not eight.
type sessionTokenCache struct {
	mu     sync.Mutex
	tokens map[string]sessionToken
	// inflight holds a per-key lock spanning the exchange so waiters reuse the
	// result instead of racing it.
	inflight map[string]*sync.Mutex
	now      func() time.Time
}

var sharedSessionTokens = newSessionTokenCache()

func newSessionTokenCache() *sessionTokenCache {
	return &sessionTokenCache{
		tokens:   map[string]sessionToken{},
		inflight: map[string]*sync.Mutex{},
		now:      time.Now,
	}
}

func (c *sessionTokenCache) token(ctx context.Context, client *grpcClient) (string, error) {
	key := sessionTokenCacheKey(client.authURL, client.apiKey)
	if value, ok := c.fresh(key); ok {
		return value, nil
	}
	exchange := c.exchangeLock(key)
	exchange.Lock()
	defer exchange.Unlock()
	// Another caller may have finished the exchange while this one waited.
	if value, ok := c.fresh(key); ok {
		return value, nil
	}
	token, err := client.exchangeAPIKey(ctx)
	if err != nil {
		return "", err
	}
	c.mu.Lock()
	c.tokens[key] = token
	c.mu.Unlock()
	return token.value, nil
}

func (c *sessionTokenCache) fresh(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	cached, ok := c.tokens[key]
	if !ok || !c.now().Before(cached.expiresAt.Add(-sessionTokenRefreshSkew)) {
		return "", false
	}
	return cached.value, true
}

func (c *sessionTokenCache) exchangeLock(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	lock, ok := c.inflight[key]
	if !ok {
		lock = &sync.Mutex{}
		c.inflight[key] = lock
	}
	return lock
}

// invalidate drops a token boxd has stopped accepting, such as after a key
// rotation, so the next call exchanges again instead of failing until the
// recorded expiry.
func (c *sessionTokenCache) invalidate(client *grpcClient, value string) {
	key := sessionTokenCacheKey(client.authURL, client.apiKey)
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.tokens[key]; ok && cached.value == value {
		delete(c.tokens, key)
	}
}

func sessionTokenCacheKey(authURL, apiKey string) string {
	sum := sha256.Sum256([]byte(authURL + "\n" + apiKey))
	return hex.EncodeToString(sum[:])
}

// connectionCache keeps one multiplexed gRPC connection per API endpoint
// because the provider interface has no lifecycle hook to close connections.
type connectionCache struct {
	mu    sync.Mutex
	conns map[string]*grpc.ClientConn
}

var sharedConnections = &connectionCache{conns: map[string]*grpc.ClientConn{}}

func (c *connectionCache) get(target string) (*grpc.ClientConn, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if conn, ok := c.conns[target]; ok {
		return conn, nil
	}
	conn, err := grpc.NewClient(
		target,
		grpc.WithTransportCredentials(credentials.NewTLS(nil)),
		grpc.WithContextDialer(dialPublic),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to boxd API %q: %w", target, err)
	}
	c.conns[target] = conn
	return conn, nil
}

func dialPublic(ctx context.Context, address string) (net.Conn, error) {
	return ssrf.Dial(ctx, &net.Dialer{Timeout: dialTimeout}, "tcp", address, false)
}
