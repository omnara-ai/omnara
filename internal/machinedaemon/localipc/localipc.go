package localipc

import (
	"context"
	"net"
)

type Listener interface {
	Accept() (net.Conn, error)
	Close() error
	Addr() net.Addr
}

func Listen(ctx context.Context, endpoint string) (Listener, error) {
	return listen(ctx, endpoint)
}

func Dial(ctx context.Context, endpoint string) (net.Conn, error) {
	return dial(ctx, endpoint)
}

func Cleanup(endpoint string) error {
	return cleanup(endpoint)
}
