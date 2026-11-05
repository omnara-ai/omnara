package metrics

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"strings"
	"testing"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	"google.golang.org/grpc/status"
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
	err = NewObservedGRPCConn(unreachable, recorder, WithHTTPClientHostLabel("boxd")).Invoke(
		context.Background(), "/svc.Api/Get", &emptypb.Empty{}, &emptypb.Empty{},
	)
	if status.Code(err) != codes.Unavailable {
		t.Fatalf("invoke error = %v, want Unavailable", err)
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

func TestObservedGRPCStreamRecordsFirstReply(t *testing.T) {
	set := New()
	intercept := NewObservedGRPCStreamInterceptor(
		NewHTTPClientRecorder(set, SubsystemHTTPClient), WithHTTPClientHostLabel("boxd"),
	)
	streamer := func(
		_ context.Context, _ *grpc.StreamDesc, _ *grpc.ClientConn, method string, _ ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		if method == "/svc.Api/Rejected" {
			return fakeClientStream{err: status.Error(codes.PermissionDenied, "denied")}, nil
		}
		return fakeClientStream{}, nil
	}
	for _, method := range []string{"/svc.Api/Exec", "/svc.Api/Rejected"} {
		stream, err := intercept(context.Background(), &grpc.StreamDesc{}, nil, method, streamer)
		if err != nil {
			t.Fatalf("open %s: %v", method, err)
		}
		if strings.Contains(scrapeMetrics(t, set), method) {
			t.Fatalf("%s recorded before its first reply", method)
		}
		_ = stream.RecvMsg(nil)
		_ = stream.RecvMsg(nil)
	}

	body := scrapeMetrics(t, set)
	for _, want := range []string{
		`omnara_http_client_requests_total{code="200",error_kind="none",host="boxd",` +
			`method="POST",path="/svc.Api/Exec",result="success"} 1`,
		`omnara_http_client_requests_total{code="none",error_kind="grpc_permission_denied",host="boxd",` +
			`method="POST",path="/svc.Api/Rejected",result="error"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics missing %q:\n%s", want, body)
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

type fakeClientStream struct {
	grpc.ClientStream
	err error
}

func (s fakeClientStream) RecvMsg(any) error {
	return s.err
}
