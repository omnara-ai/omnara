package boxd

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/golang-lru/v2/simplelru"
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
	apiURL                  = "boxd.sh:9443"
	authURL                 = "https://app.boxd.sh/api/v1/auth/token"
	sessionTokenRefreshSkew = 5 * time.Minute
	maxSessionTokens        = 1_024
	bearerPrefix            = "Bearer "
	dialTimeout             = 10 * time.Second
	execStdinChunkBytes     = 32 * 1024
)

type apiClient interface {
	GetVM(context.Context, string) (vm, bool, error)
	ListVMs(context.Context) ([]vm, error)
	CreateVM(context.Context, createVMRequest) (vm, error)
	DestroyVM(context.Context, string) error
	Exec(context.Context, string, string, []byte) (execResult, error)
	GetSnapshot(context.Context, string) (snapshotInfo, bool, error)
	GetOrgMachineDefaults(context.Context) (machineSize, error)
}

type vmStatus string

type vm struct {
	ID           string
	Name         string
	Status       vmStatus
	VCPU         int
	MemoryBytes  uint64
	AccessDomain string
}

func (v vm) url() string {
	if v.Name == "" || v.AccessDomain == "" {
		return ""
	}
	return "https://" + v.Name + "." + v.AccessDomain + "/"
}

type createVMRequest struct {
	Name                   string
	Snapshot               string
	VCPU                   int
	MemoryBytes            uint64
	AutoSuspendTimeoutSecs uint32
}

type execResult struct {
	ExitCode int
}

type snapshotInfo struct {
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

func isRejectedRequest(err error) bool {
	var apiErr apiError
	if !errors.As(err, &apiErr) {
		return false
	}
	switch apiErr.Code {
	case codes.InvalidArgument,
		codes.PermissionDenied,
		codes.NotFound,
		codes.OutOfRange,
		codes.Unimplemented:
		return true
	default:
		return false
	}
}

type grpcClient struct {
	authURL    string
	apiKey     string
	httpClient *http.Client
	stub       boxdv1.BoxdApiClient
	tokens     *sessionTokenCache
}

func newGRPCClient(apiKey string) *grpcClient {
	return &grpcClient{
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
	conn, err := sharedConnection()
	if err != nil {
		return nil, err
	}
	return boxdv1.NewBoxdApiClient(conn), nil
}

func (c *grpcClient) session(ctx context.Context) (boxdv1.BoxdApiClient, context.Context, error) {
	api, err := c.api()
	if err != nil {
		return nil, nil, err
	}
	token, err := c.tokens.token(ctx, c)
	if err != nil {
		return nil, nil, err
	}
	return api, metadata.AppendToOutgoingContext(ctx, "authorization", bearerPrefix+token), nil
}

func (c *grpcClient) rpcError(ctx context.Context, err error) error {
	err = fromGRPC(err)
	if isCode(err, codes.Unauthenticated) {
		md, _ := metadata.FromOutgoingContext(ctx)
		for _, authorization := range md.Get("authorization") {
			c.tokens.evict(c, strings.TrimPrefix(authorization, bearerPrefix))
		}
	}
	return err
}

func (c *grpcClient) GetVM(ctx context.Context, ref string) (vm, bool, error) {
	api, ctx, err := c.session(ctx)
	if err != nil {
		return vm{}, false, err
	}
	response, err := api.GetVm(ctx, &boxdv1.GetVmRequest{VmId: ref})
	if err != nil {
		err = c.rpcError(ctx, err)
		if isNotFound(err) {
			return vm{}, false, nil
		}
		return vm{}, false, err
	}
	return vmFromResponse(response), true, nil
}

func (c *grpcClient) ListVMs(ctx context.Context) ([]vm, error) {
	api, ctx, err := c.session(ctx)
	if err != nil {
		return nil, err
	}
	response, err := api.ListVms(ctx, &boxdv1.ListVmsRequest{})
	if err != nil {
		return nil, c.rpcError(ctx, err)
	}
	vms := make([]vm, 0, len(response.GetVms()))
	for _, item := range response.GetVms() {
		vms = append(vms, vmFromResponse(item))
	}
	return vms, nil
}

func (c *grpcClient) CreateVM(ctx context.Context, request createVMRequest) (vm, error) {
	api, ctx, err := c.session(ctx)
	if err != nil {
		return vm{}, err
	}
	config := &boxdv1.VmConfig{
		Srf: &boxdv1.SrfConfig{AutoSuspendTimeoutSecs: proto.Uint32(request.AutoSuspendTimeoutSecs)},
	}
	var response *boxdv1.CreateVmResponse
	if request.Snapshot != "" {
		response, err = api.CreateVmFromSnapshot(ctx, &boxdv1.CreateVmFromSnapshotRequest{
			Snapshot: request.Snapshot,
			Name:     request.Name,
			Config:   config,
			Isolated: true,
		})
	} else {
		config.Vcpu = uint32(request.VCPU)
		config.MemoryBytes = request.MemoryBytes
		response, err = api.CreateVm(ctx, &boxdv1.CreateVmRequest{
			Name:     request.Name,
			Config:   config,
			Isolated: true,
		})
	}
	if err != nil {
		return vm{}, c.rpcError(ctx, err)
	}
	return vm{
		ID:     response.GetVmId(),
		Name:   response.GetName(),
		Status: vmStatus(response.GetStatus()),
	}, nil
}

func (c *grpcClient) DestroyVM(ctx context.Context, ref string) error {
	api, ctx, err := c.session(ctx)
	if err != nil {
		return err
	}
	if _, err := api.DestroyVm(ctx, &boxdv1.DestroyVmRequest{VmId: ref}); err != nil {
		err = c.rpcError(ctx, err)
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
	api, ctx, err := c.session(ctx)
	if err != nil {
		return execResult{}, err
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	stream, err := api.Exec(ctx)
	if err != nil {
		return execResult{}, c.rpcError(ctx, err)
	}
	if err := sendExecInput(stream, ref, command, stdin); err != nil {
		return execResult{}, c.rpcError(ctx, err)
	}
	var result execResult
	received := false
	for {
		chunk, err := stream.Recv()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return execResult{}, c.rpcError(ctx, err)
		}
		received = true
		if exitCode := chunk.GetExitCode(); exitCode != 0 {
			result.ExitCode = int(exitCode)
		}
	}
	if !received {
		return execResult{}, errors.New("boxd exec ended without a response")
	}
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
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
	return stream.CloseSend()
}

func (c *grpcClient) GetSnapshot(ctx context.Context, name string) (snapshotInfo, bool, error) {
	api, ctx, err := c.session(ctx)
	if err != nil {
		return snapshotInfo{}, false, err
	}
	response, err := api.GetSnapshot(ctx, &boxdv1.GetSnapshotRequest{Name: name})
	if err != nil {
		err = c.rpcError(ctx, err)
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
		Status:      snapshot.GetStatus(),
		VCPU:        int(snapshot.GetVcpu()),
		MemoryBytes: snapshot.GetMemoryBytes(),
	}, true, nil
}

func (c *grpcClient) GetOrgMachineDefaults(ctx context.Context) (machineSize, error) {
	api, ctx, err := c.session(ctx)
	if err != nil {
		return machineSize{}, err
	}
	response, err := api.GetOrgMachineDefaults(ctx, &boxdv1.GetOrgMachineDefaultsRequest{})
	if err != nil {
		return machineSize{}, c.rpcError(ctx, err)
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

func (c *grpcClient) exchangeAPIKey(ctx context.Context) (string, time.Time, error) {
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
		return "", time.Time{}, apiError{Code: codes.Unavailable, Message: err.Error(), cause: err}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		code := codes.Unauthenticated
		if response.StatusCode == http.StatusTooManyRequests || response.StatusCode >= http.StatusInternalServerError {
			code = codes.Unavailable
		}
		return "", time.Time{}, providers.WithRetryAfter(
			apiError{Code: code, Message: fmt.Sprintf("token exchange returned HTTP %d", response.StatusCode)},
			response.Header,
		)
	}
	var body struct {
		Token     string `json:"token"`
		ExpiresAt int64  `json:"expires_at"`
	}
	if err := json.Unmarshal(response.Body, &body); err != nil {
		return "", time.Time{}, fmt.Errorf("decode boxd token exchange response: %w", err)
	}
	if body.Token == "" || body.ExpiresAt <= 0 {
		return "", time.Time{}, errors.New("boxd token exchange response is missing the token or its expiry")
	}
	return body.Token, time.Unix(body.ExpiresAt, 0), nil
}

type sessionToken struct {
	mu        sync.Mutex
	value     string
	expiresAt time.Time
}

type sessionTokenCache struct {
	mu     sync.Mutex
	tokens *simplelru.LRU[string, *sessionToken]
	now    func() time.Time
}

var sharedSessionTokens = newSessionTokenCache()

func newSessionTokenCache() *sessionTokenCache {
	tokens, _ := simplelru.NewLRU[string, *sessionToken](maxSessionTokens, nil)
	return &sessionTokenCache{tokens: tokens, now: time.Now}
}

func sessionTokenKey(client *grpcClient) string {
	sum := sha256.Sum256([]byte(client.authURL + "\n" + client.apiKey))
	return hex.EncodeToString(sum[:])
}

func (c *sessionTokenCache) evict(client *grpcClient, rejected string) {
	c.mu.Lock()
	entry, ok := c.tokens.Peek(sessionTokenKey(client))
	c.mu.Unlock()
	if !ok {
		return
	}
	entry.mu.Lock()
	if entry.value == rejected {
		entry.value, entry.expiresAt = "", time.Time{}
	}
	entry.mu.Unlock()
}

func (c *sessionTokenCache) token(ctx context.Context, client *grpcClient) (string, error) {
	key := sessionTokenKey(client)
	c.mu.Lock()
	entry, ok := c.tokens.Get(key)
	if !ok {
		entry = &sessionToken{}
		c.tokens.Add(key, entry)
	}
	c.mu.Unlock()
	entry.mu.Lock()
	defer entry.mu.Unlock()
	if c.now().Before(entry.expiresAt) {
		return entry.value, nil
	}
	value, expiresAt, err := client.exchangeAPIKey(ctx)
	if err != nil {
		return "", err
	}
	entry.value = value
	entry.expiresAt = expiresAt.Add(-sessionTokenRefreshSkew)
	return value, nil
}

var sharedConnection = sync.OnceValues(func() (*grpc.ClientConn, error) {
	conn, err := grpc.NewClient(
		apiURL,
		grpc.WithTransportCredentials(credentials.NewTLS(nil)),
		grpc.WithContextDialer(dialPublic),
	)
	if err != nil {
		return nil, fmt.Errorf("connect to boxd API %q: %w", apiURL, err)
	}
	return conn, nil
})

func dialPublic(ctx context.Context, address string) (net.Conn, error) {
	return ssrf.Dial(ctx, &net.Dialer{Timeout: dialTimeout}, "tcp", address, false)
}
