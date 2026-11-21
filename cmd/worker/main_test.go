package main

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"testing/synctest"
	"time"

	"github.com/omnara-ai/omnara/internal/config"
	"github.com/omnara-ai/omnara/internal/ssrf"
	"github.com/omnara-ai/omnara/internal/webaccess"
)

func TestWorkerDatabaseMaxConns(t *testing.T) {
	for _, test := range []struct {
		capacity int
		want     int32
	}{
		{capacity: 1, want: 10},
		{capacity: 4, want: 10},
		{capacity: 64, want: 10},
		{capacity: 80, want: 10},
		{capacity: 81, want: 11},
		{capacity: 128, want: 16},
		{capacity: 129, want: 17},
		{capacity: 256, want: 32},
		{capacity: 257, want: 33},
		{capacity: math.MaxInt32, want: 268435456},
	} {
		t.Run(strconv.Itoa(test.capacity), func(t *testing.T) {
			if got := workerDatabaseMaxConns(test.capacity); got != test.want {
				t.Fatalf("max conns = %d, want %d", got, test.want)
			}
		})
	}
	if strconv.IntSize == 64 && workerDatabaseMaxConns(math.MaxInt) != math.MaxInt32 {
		t.Fatal("max conns should be capped at the pool's int32 limit")
	}
}

func TestCancelOnSignalsDrainsOnFirstSignalAndAbortsOnSecond(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		signals := make(chan os.Signal)
		calls := make(chan string, 2)
		go cancelOnSignals(signals, func() { calls <- "drain" }, func() { calls <- "abort" })

		signals <- syscall.SIGTERM
		synctest.Wait()
		if got := received(calls); !slices.Equal(got, []string{"drain"}) {
			t.Fatalf("after the first signal: %v, want [drain]", got)
		}
		signals <- syscall.SIGTERM
		synctest.Wait()
		if got := received(calls); !slices.Equal(got, []string{"abort"}) {
			t.Fatalf("after the second signal: %v, want [abort]", got)
		}
	})
}

func received(calls chan string) []string {
	var got []string
	for len(calls) > 0 {
		got = append(got, <-calls)
	}
	return got
}

func TestCancelAfterDone(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		parent, endParent := context.WithCancel(t.Context())
		ctx, cancel := cancelAfterDone(parent, 20*time.Second)
		defer cancel()
		time.Sleep(time.Hour) //nolint:omnaralint // Advance synctest's virtual clock.
		endParent()
		parentEndedAt := time.Now()
		<-ctx.Done()
		if elapsed := time.Since(parentEndedAt); elapsed != 20*time.Second {
			t.Fatalf("context ended %s after its parent, want 20s", elapsed)
		}

		ctx, cancel = cancelAfterDone(t.Context(), time.Hour)
		cancel()
		if !errors.Is(ctx.Err(), context.Canceled) {
			t.Fatalf("context error after cancel = %v, want canceled", ctx.Err())
		}
	})
}

func TestOutboundClientsKeepGrantsSeparate(t *testing.T) {
	endpoint := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer endpoint.Close()
	fetched := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("fetched"))
	}))
	defer fetched.Close()
	listed := func(server *httptest.Server) *ssrf.Allowlist {
		t.Helper()
		allowlist, err := ssrf.ParseAllowlist([]string{server.Listener.Addr().String()})
		if err != nil {
			t.Fatal(err)
		}
		return allowlist
	}
	mcpAuth, fetcher := newOutboundClients(config.Config{
		EndpointAllowlist: listed(endpoint), WebFetchAllowlist: listed(fetched),
	})
	get := func(client *http.Client, target string) error {
		response, err := client.Get(target)
		if err == nil {
			_ = response.Body.Close()
		}
		return err
	}
	fetch := func(target string) error {
		_, err := fetcher.Fetch(context.Background(), webaccess.FetchRequest{URL: target})
		return err
	}
	if err := get(mcpAuth.EndpointHTTPClient, endpoint.URL); err != nil {
		t.Fatalf("MCP endpoint client to its listed address: %v", err)
	}
	if err := get(mcpAuth.EndpointHTTPClient, fetched.URL); !errors.Is(err, ssrf.ErrBlockedAddress) {
		t.Fatalf("MCP endpoint client to a web fetch address error = %v, want ErrBlockedAddress", err)
	}
	if err := fetch(fetched.URL); err != nil {
		t.Fatalf("web fetcher to its listed address: %v", err)
	}
	err := fetch(endpoint.URL)
	if providerErr, ok := webaccess.AsProviderError(err); !ok || providerErr.Code != webaccess.ErrorCodeFetchBlocked {
		t.Fatalf("web fetcher to an endpoint address error = %v, want %s", err, webaccess.ErrorCodeFetchBlocked)
	}
	if err := get(mcpAuth.HTTPClient, endpoint.URL); !errors.Is(err, ssrf.ErrBlockedAddress) {
		t.Fatalf("OAuth client plain HTTP outside development mode error = %v, want ErrBlockedAddress", err)
	}
}
