package ssrf

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"time"
)

type TransportOptions struct {
	AllowLoopback                bool
	DialTimeout                  time.Duration
	TLSHandshakeTimeout          time.Duration
	ResponseHeaderTimeout        time.Duration
	DisableResponseHeaderTimeout bool
	IdleConnTimeout              time.Duration
}

func NewTransport(opts TransportOptions) *http.Transport {
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
	return &http.Transport{
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			return Dial(ctx, dialer, network, addr, opts.AllowLoopback)
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
}

func Dial(ctx context.Context, dialer *net.Dialer, network, addr string, allowLoopback bool) (net.Conn, error) {
	host, port, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); ip != nil {
		if !IsAllowedIP(ip, allowLoopback) {
			return nil, fmt.Errorf("%w: %s", ErrBlockedAddress, ip)
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
	}
	addrs, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return nil, err
	}
	for _, a := range addrs {
		if IsAllowedIP(a.IP, allowLoopback) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(a.IP.String(), port))
		}
	}
	return nil, fmt.Errorf("%w: %s (no allowed IPs)", ErrBlockedAddress, host)
}
