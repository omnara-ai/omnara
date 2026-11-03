package metrics

import (
	"context"
	"errors"
	"testing"
)

func TestReadyAllCallsChecksInOrder(t *testing.T) {
	var calls []string
	ready := ReadyAll(
		func(context.Context) error {
			calls = append(calls, "db")
			return nil
		},
		func(context.Context) error {
			calls = append(calls, "redis")
			return nil
		},
	)

	if err := ready(context.Background()); err != nil {
		t.Fatalf("ready error = %v, want nil", err)
	}
	if len(calls) != 2 || calls[0] != "db" || calls[1] != "redis" {
		t.Fatalf("calls = %v, want db then redis", calls)
	}
}

func TestReadyAllReturnsFirstFailure(t *testing.T) {
	dbErr := errors.New("db unavailable")
	calledRedis := false
	ready := ReadyAll(
		func(context.Context) error {
			return dbErr
		},
		func(context.Context) error {
			calledRedis = true
			return nil
		},
	)

	if err := ready(context.Background()); !errors.Is(err, dbErr) {
		t.Fatalf("ready error = %v, want db error", err)
	}
	if calledRedis {
		t.Fatal("redis check ran after db failure")
	}
}

func TestReadyAllReturnsRedisFailure(t *testing.T) {
	redisErr := errors.New("redis unavailable")
	ready := ReadyAll(
		func(context.Context) error {
			return nil
		},
		func(context.Context) error {
			return redisErr
		},
	)

	if err := ready(context.Background()); !errors.Is(err, redisErr) {
		t.Fatalf("ready error = %v, want redis error", err)
	}
}
