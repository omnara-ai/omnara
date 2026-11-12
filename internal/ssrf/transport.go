package ssrf

import (
	"cmp"
	"context"
	"net"
	"net/http"
	"slices"
	"strconv"
	"time"
)

type PlainHTTPPolicy uint8

const (
	PlainHTTPUnrestricted PlainHTTPPolicy = iota
	PlainHTTPAllowlistOnly
	PlainHTTPLoopbackOnly
)

type TransportOptions struct {
	AllowLoopback                bool
	Allowlist                    *Allowlist
	PlainHTTP                    PlainHTTPPolicy
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
	if opts.PlainHTTP == PlainHTTPUnrestricted {
		return transport
	}
	return plainHTTPMarker{Transport: transport, policy: opts.PlainHTTP}
}

type plainHTTPKey struct{}

type plainHTTPMarker struct {
	*http.Transport
	policy PlainHTTPPolicy
}

func (t plainHTTPMarker) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme == "http" {
		req = req.WithContext(context.WithValue(req.Context(), plainHTTPKey{}, t.policy))
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
	policy, _ := ctx.Value(plainHTTPKey{}).(PlainHTTPPolicy)
	if policy == PlainHTTPLoopbackOnly {
		allowlist = nil
	}
	plainHTTP := policy != PlainHTTPUnrestricted
	permitted := func(ip net.IP) bool {
		if !plainHTTP {
			return allowlist.PermitsIP(ip, uint16(portNumber), allowLoopback)
		}
		return allowlist.Contains(ip, uint16(portNumber)) || (allowLoopback && ip.IsLoopback())
	}
	if ip := net.ParseIP(host); ip != nil {
		if !permitted(ip) {
			return nil, allowlist.blocked(ip.String(), port, true, plainHTTP)
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	addrs, err := cmp.Or(dialer.Resolver, net.DefaultResolver).LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	addrs = slices.DeleteFunc(addrs, func(a net.IPAddr) bool { return !permitted(a.IP) })
	if len(addrs) == 0 {
		return nil, allowlist.blocked(host, port, false, plainHTTP)
	}
	if dialer.Timeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, dialer.Timeout)
		defer cancel()
	}
	deadline, hasDeadline := ctx.Deadline()
	var dialErr error
	for i, a := range addrs {
		attempt := *dialer
		if hasDeadline {
			attempt.Timeout = max(time.Until(deadline)/time.Duration(len(addrs)-i), 2*time.Second)
		}
		conn, err := attempt.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		if err == nil {
			return conn, nil
		}
		dialErr = err
	}
	return nil, dialErr
}
