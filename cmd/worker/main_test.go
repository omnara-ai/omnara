package main

import (
	"math"
	"strconv"
	"testing"
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
