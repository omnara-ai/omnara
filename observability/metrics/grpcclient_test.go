package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/types/known/emptypb"

	log "github.com/omnara-ai/omnara/observability/wideevent"
)

func TestObservedGRPCRecordsCalls(t *testing.T) {
	set := New()
	recorder := NewHTTPClientRecorder(set, SubsystemHTTPClient)
	unreachable, err := grpc.NewClient(
		"passthrough:///boxd.test",
		grpc.WithTransportCredentials(insecure.NewCredentials()),
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return nil, errors.New("down") }),
	)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer unreachable.Close()
	boxd := NewObservedGRPCConn(unreachable, recorder, WithHTTPClientHostLabel("boxd"))
	err = boxd.Invoke(context.Background(), "/svc.Api/Get", &emptypb.Empty{}, &emptypb.Empty{})
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("invoke error = %v, want Unavailable", err)
	}
	_, err = boxd.NewStream(context.Background(), &grpc.StreamDesc{ServerStreams: true}, "/svc.Api/Watch")
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("open stream error = %v, want Unavailable", err)
	}

	target, err := grpc.NewClient("api.example.com:443", grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	defer target.Close()
	ok := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error { return nil }
	if err := NewObservedGRPCUnaryInterceptor(recorder)(
		context.Background(), "/svc.Api/Create", nil, nil, target, ok,
	); err != nil {
		t.Fatalf("unary call: %v", err)
	}
	denied := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return status.Error(codes.PermissionDenied, "denied")
	}
	if err := NewObservedGRPCUnaryInterceptor(recorder, WithHTTPClientHostLabel("modal"))(
		context.Background(), "/svc.Api/Delete", nil, nil, target, denied,
	); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("unary error = %v, want PermissionDenied", err)
	}

	body := scrapeMetrics(t, set)
	for _, want := range []string{
		`omnara_http_client_requests_total{code="none",error_kind="grpc_unavailable",host="boxd",` +
			`method="POST",path="/svc.Api/Get",result="error"} 1`,
		`omnara_http_client_requests_total{code="none",error_kind="grpc_unavailable",host="boxd",` +
			`method="POST",path="/svc.Api/Watch",result="error"} 1`,
		`omnara_http_client_requests_total{code="200",error_kind="none",host="api.example.com:443",` +
			`method="POST",path="/svc.Api/Create",result="success"} 1`,
		`omnara_http_client_requests_total{code="none",error_kind="grpc_permission_denied",host="modal",` +
			`method="POST",path="/svc.Api/Delete",result="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
		}
	}
}

func TestObservedGRPCStreamsRecordFinalStatus(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer(grpc.UnknownServiceHandler(func(_ any, stream grpc.ServerStream) error {
		if err := stream.SendMsg(&emptypb.Empty{}); err != nil {
			return err
		}
		switch method, _ := grpc.MethodFromServerStream(stream); method {
		case "/svc.Api/FailsAfterReply":
			return status.Error(codes.Unavailable, "dropped")
		case "/svc.Api/Waits":
			<-stream.Context().Done()
			return stream.Context().Err()
		default:
			return nil
		}
	}))
	go func() { _ = server.Serve(listener) }()
	defer server.Stop()
	dial := func(opts ...grpc.DialOption) *grpc.ClientConn {
		conn, err := grpc.NewClient("passthrough:///bufnet", append([]grpc.DialOption{
			grpc.WithTransportCredentials(insecure.NewCredentials()),
			grpc.WithContextDialer(func(ctx context.Context, _ string) (net.Conn, error) {
				return listener.DialContext(ctx)
			}),
		}, opts...)...)
		if err != nil {
			t.Fatalf("new client: %v", err)
		}
		t.Cleanup(func() { _ = conn.Close() })
		return conn
	}
	set := New()
	recorder := NewHTTPClientRecorder(set, SubsystemHTTPClient)
	conns := []struct {
		host string
		conn grpc.ClientConnInterface
	}{
		{"boxd", NewObservedGRPCConn(dial(), recorder, WithHTTPClientHostLabel("boxd"))},
		{"modal", dial(grpc.WithStreamInterceptor(
			NewObservedGRPCStreamInterceptor(recorder, WithHTTPClientHostLabel("modal")),
		))},
	}

	calls := []struct{ path, code, kind, result string }{
		{"/svc.Api/Completes", "200", "none", "success"},
		{"/svc.Api/FailsAfterReply", "none", "grpc_unavailable", "error"},
		{"/svc.Api/Waits", "none", "context_canceled", "error"},
	}
	for _, call := range calls {
		for _, client := range conns {
			ctx, cancel := context.WithCancel(context.Background())
			finished := make(chan struct{})
			stream, err := client.conn.NewStream(
				ctx,
				&grpc.StreamDesc{ServerStreams: true, ClientStreams: true},
				call.path,
				grpc.OnFinish(func(error) { close(finished) }),
			)
			if err != nil {
				t.Fatalf("%s %s: open: %v", client.host, call.path, err)
			}
			if err := stream.CloseSend(); err != nil {
				t.Fatalf("%s %s: close send: %v", client.host, call.path, err)
			}
			if err := stream.RecvMsg(&emptypb.Empty{}); err != nil {
				t.Fatalf("%s %s: first reply: %v", client.host, call.path, err)
			}
			if call.path != "/svc.Api/Waits" {
				_ = stream.RecvMsg(&emptypb.Empty{})
			}
			cancel()
			<-finished
		}
	}

	body := scrapeMetrics(t, set)
	for _, call := range calls {
		for _, client := range conns {
			want := fmt.Sprintf(
				`omnara_http_client_requests_total{code=%q,error_kind=%q,host=%q,method="POST",path=%q,result=%q} 1`,
				call.code, call.kind, client.host, call.path, call.result,
			)
			if !strings.Contains(body, want) {
				t.Fatalf("metrics missing %q:\n%s", want, body)
			}
		}
	}
}

func TestObservedGRPCErrorKeepsStatusInLogRecord(t *testing.T) {
	var buf bytes.Buffer
	ctx := log.WithLogger(context.Background(), slog.New(slog.NewJSONHandler(&buf, nil)))
	event := log.NewEvent(ctx, "test.event")
	ctx = log.WithEvent(ctx, event)
	recorder := NewHTTPClientRecorder(New(), SubsystemHTTPClient)
	denied := func(context.Context, string, any, any, *grpc.ClientConn, ...grpc.CallOption) error {
		return status.Error(codes.PermissionDenied, "denied")
	}
	_ = NewObservedGRPCUnaryInterceptor(recorder, WithHTTPClientHostLabel("boxd"))(
		ctx, "/svc.Api/Get", nil, nil, nil, denied,
	)
	event.Done(context.Background())

	var record map[string]any
	if err := json.Unmarshal(bytes.TrimSpace(buf.Bytes()), &record); err != nil {
		t.Fatalf("decode log record: %v\n%s", err, buf.String())
	}
	if got := record["http.subrequests.0.error"]; got != "PermissionDenied: denied" {
		t.Fatalf("http.subrequests.0.error = %v, want PermissionDenied: denied", got)
	}
}
