package ssrf

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"time"
)

type TransportOptions struct {
	AllowLoopback                bool
	Allowlist                    *Allowlist
	DialTimeout                  time.Duration
	TLSHandshakeTimeout          time.Duration
	ResponseHeaderTimeout        time.Duration
	DisableResponseHeaderTimeout bool
	IdleConnTimeout              time.Duration
}

func NewTransport(opts TransportOptions) http.RoundTripper {
	if opts.DialTimeout == 0 {
		opts.DialTimeout = 5 * time.Second
	}
	if opts.TLSHandshakeTimeout == 0 {
		opts.TLSHandshakeTimeout = 10 * time.Second
	}
	if opts.ResponseHeaderTimeout == 0 && !opts.DisableResponseHeaderTimeout {
		opts.ResponseHeaderTimeout = 30 * time.Second
	}
	if opts.IdleConnTimeout == 0 {
		opts.IdleConnTimeout = 90 * time.Second
	}
	dialer := &net.Dialer{Timeout: opts.DialTimeout, KeepAlive: 30 * time.Second}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return dial(ctx, dialer, network, addr, opts.AllowLoopback, opts.Allowlist)
		},
		TLSHandshakeTimeout:   opts.TLSHandshakeTimeout,
		ResponseHeaderTimeout: opts.ResponseHeaderTimeout,
		IdleConnTimeout:       opts.IdleConnTimeout,
		ForceAttemptHTTP2:     true,
		HTTP2: &http.HTTP2Config{
			SendPingTimeout: 15 * time.Second,
			PingTimeout:     5 * time.Second,
		},
	}
	if opts.Allowlist == nil || !opts.Allowlist.plainHTTPHostnames {
		return transport
	}
	return plainHTTPMarker{transport}
}

type plainHTTPKey struct{}

type plainHTTPMarker struct{ *http.Transport }

func (t plainHTTPMarker) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "http" {
		req = req.WithContext(context.WithValue(req.Context(), plainHTTPKey{}, true))
	}
	return t.Transport.RoundTrip(req)
}

func Dial(ctx context.Context, dialer *net.Dialer, network, addr string, allowLoopback bool) (net.Conn, error) {
	return dial(ctx, dialer, network, addr, allowLoopback, nil)
}

func dial(
	ctx context.Context, dialer *net.Dialer, network, addr string, allowLoopback bool, allowlist *Allowlist,
) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	portNumber, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		return nil, err
	}
	plainHTTP := ctx.Value(plainHTTPKey{}) != nil
	permitted := func(ip net.IP) bool {
		return allowlist.permits(ip, uint16(portNumber), allowLoopback, plainHTTP)
	}
	if ip := net.ParseIP(host); ip != nil {
		if !permitted(ip) {
			return nil, allowlist.blocked(ip.String(), port, true, plainHTTP)
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if permitted(a.IP) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		}
	}
	return nil, allowlist.blocked(host, port, false, plainHTTP)
}
