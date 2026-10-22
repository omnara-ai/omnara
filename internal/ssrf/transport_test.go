package ssrf

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestTransportIdleOptions(t *testing.T) {
	transport := NewTransport(TransportOptions{
		MaxIdleConnsPerHost: 64, MaxIdleConns: 128, IdleConnTimeout: time.Minute,
	})
	require.Equal(t, 64, transport.MaxIdleConnsPerHost)
	require.Equal(t, 128, transport.MaxIdleConns)
	require.Equal(t, time.Minute, transport.IdleConnTimeout)
	require.Equal(t, 30*time.Second, transport.ResponseHeaderTimeout)
	require.Equal(t, 90*time.Second, NewTransport(TransportOptions{}).IdleConnTimeout)
}
