package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

type defaultModelProviderProvisioningRunnerStub func(context.Context) (bool, string, error)

func (stub defaultModelProviderProvisioningRunnerStub) RunNext(
	ctx context.Context,
) (bool, string, error) {
	return stub(ctx)
}

func TestDefaultModelProviderProvisioningTickRecoversPanic(t *testing.T) {
	runner := defaultModelProviderProvisioningRunnerStub(func(context.Context) (bool, string, error) {
		panic("provider panic")
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	if _, _, err := runDefaultModelProviderProvisioningTick(
		context.Background(),
		log,
		runner,
	); err == nil {
		t.Fatal("provisioning tick panic error = nil")
	}
}
