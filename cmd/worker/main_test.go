package main

import (
	"context"
	"errors"
	"math"
	"os"
	"slices"
	"strconv"
	"syscall"
	"testing"
	"testing/synctest"
	"time"
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
