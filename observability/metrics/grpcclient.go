package metrics

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
	"unicode"

	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

func NewObservedGRPCConn(
	conn *grpc.ClientConn,
	recorder *HTTPClientRecorder,
	opts ...HTTPClientObserverOption,
) grpc.ClientConnInterface {
	if recorder == nil {
		return conn
	}
	return observedGRPCConn{conn: conn, observer: newClientObserver(recorder, opts)}
}

func NewObservedGRPCUnaryInterceptor(
	recorder *HTTPClientRecorder,
	opts ...HTTPClientObserverOption,
) grpc.UnaryClientInterceptor {
	observer := newClientObserver(recorder, opts)
	return func(
		ctx context.Context,
		method string,
		req, reply any,
		cc *grpc.ClientConn,
		invoker grpc.UnaryInvoker,
		callOpts ...grpc.CallOption,
	) error {
		start := observer.now()
		err := invoker(ctx, method, req, reply, cc, callOpts...)
		observer.recordGRPC(ctx, cc, method, start, err)
		return err
	}
}

func NewObservedGRPCStreamInterceptor(
	recorder *HTTPClientRecorder,
	opts ...HTTPClientObserverOption,
) grpc.StreamClientInterceptor {
	observer := newClientObserver(recorder, opts)
	return func(
		ctx context.Context,
		desc *grpc.StreamDesc,
		cc *grpc.ClientConn,
		method string,
		streamer grpc.Streamer,
		callOpts ...grpc.CallOption,
	) (grpc.ClientStream, error) {
		return streamer(ctx, desc, cc, method, observer.recordOnFinish(ctx, cc, method, callOpts)...)
	}
}

type observedGRPCConn struct {
	conn     *grpc.ClientConn
	observer clientObserver
}

func (c observedGRPCConn) Invoke(ctx context.Context, method string, args, reply any, opts ...grpc.CallOption) error {
	start := c.observer.now()
	err := c.conn.Invoke(ctx, method, args, reply, opts...)
	c.observer.recordGRPC(ctx, c.conn, method, start, err)
	return err
}

func (c observedGRPCConn) NewStream(
	ctx context.Context,
	desc *grpc.StreamDesc,
	method string,
	opts ...grpc.CallOption,
) (grpc.ClientStream, error) {
	return c.conn.NewStream(ctx, desc, method, c.observer.recordOnFinish(ctx, c.conn, method, opts)...)
}

func (o clientObserver) recordOnFinish(
	ctx context.Context,
	cc *grpc.ClientConn,
	method string,
	opts []grpc.CallOption,
) []grpc.CallOption {
	start := o.now()
	if start.IsZero() {
		return opts
	}
	return append([]grpc.CallOption{grpc.OnFinish(func(err error) {
		o.recordGRPC(ctx, cc, method, start, err)
	})}, opts...)
}

func (o clientObserver) recordGRPC(
	ctx context.Context,
	cc *grpc.ClientConn,
	method string,
	start time.Time,
	err error,
) {
	if start.IsZero() {
		return
	}
	request := HTTPRequestData{Method: http.MethodPost, Host: o.hostLabel, Path: o.pathLabel}
	if request.Host == "" && cc != nil {
		request.Host = cc.Target()
	}
	if request.Path == "" {
		request.Path = method
	}
	if err != nil {
		grpcStatus := status.Convert(err)
		request.ErrorKind = grpcErrorKind(grpcStatus.Code())
		err = errors.New(grpcStatus.Code().String() + ": " + grpcStatus.Message())
	} else {
		request.StatusCode = http.StatusOK
	}
	o.recorder.recordCall(ctx, request, start, err)
}

func grpcErrorKind(code codes.Code) string {
	switch code {
	case codes.Canceled:
		return "context_canceled"
	case codes.DeadlineExceeded:
		return "context_deadline_exceeded"
	default:
		var kind strings.Builder
		kind.WriteString("grpc")
		for _, r := range code.String() {
			if unicode.IsUpper(r) {
				kind.WriteByte('_')
			}
			kind.WriteRune(unicode.ToLower(r))
		}
		return kind.String()
	}
}
